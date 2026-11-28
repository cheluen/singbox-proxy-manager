import argparse
import importlib.util
import struct
import tempfile
import unittest
import zlib
from pathlib import Path


MODULE_PATH = Path(__file__).resolve().parent / "build-ipgeo-db.py"
SPEC = importlib.util.spec_from_file_location("build_ipgeo_db", MODULE_PATH)
BUILD = importlib.util.module_from_spec(SPEC)
assert SPEC is not None and SPEC.loader is not None
SPEC.loader.exec_module(BUILD)


def read_uvarint(data, position):
    value = 0
    shift = 0
    while True:
        current = data[position]
        position += 1
        value |= (current & 0x7F) << shift
        if current < 0x80:
            return value, position
        shift += 7


def decode_sbg2(data):
    assert data[:4] == b"SBG2"
    country_count, flags, v4_count, v6_count = struct.unpack(">BBHH", data[4:10])
    position = 10 + country_count * 2
    indexes = []
    for _ in range(v4_count):
        indexes.append(struct.unpack(">II", data[position : position + 8]))
        position += 8
    for _ in range(v6_count):
        indexes.append(struct.unpack(">QI", data[position : position + 12]))
        position += 12
    end_offset = struct.unpack(">I", data[position : position + 4])[0]
    blocks = []
    for index, (first_key, offset) in enumerate(indexes):
        end = indexes[index + 1][1] if index + 1 < len(indexes) else end_offset
        raw = zlib.decompress(data[offset:end], wbits=-15)
        country = raw[0]
        key = first_key
        entries = [(key, country)]
        position = 1
        while position < len(raw):
            delta, position = read_uvarint(raw, position)
            entries.append((key + delta, raw[position]))
            position += 1
            key += delta
        blocks.append(entries)
    return countries(data[10 : 10 + country_count * 2]), flags, blocks


def countries(data):
    return [data[index : index + 2].decode("ascii") for index in range(0, len(data), 2)]


class BuildIPGeoDBTest(unittest.TestCase):
    def test_parse_ranges_merges_and_clips(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "ranges.csv"
            path.write_text("0,511,us\n256,1023,US\n1024,1279,CN\n", encoding="utf-8")
            self.assertEqual(BUILD.parse_ranges(path, shift=8, max_key=3), [(0, 3, "US")])

    def test_build_transitions_merges_state_changes(self):
        ranges = [(2, 4, "US"), (5, 6, "US"), (9, 9, "CN")]
        self.assertEqual(
            BUILD.build_transitions(ranges, {"CN": 1, "US": 2}, 10),
            [(2, 2), (7, 0), (9, 1), (10, 0)],
        )

    def test_sbg2_round_trip_and_block_boundaries(self):
        data = BUILD.build_sbg2(
            ["CN", "US"], [(1, 1), (4, 0), (7, 2)], [(3, 2)], block_size=2
        )
        self.assertEqual(data[:4], b"SBG2")
        self.assertEqual(decode_sbg2(data), (["CN", "US"], 0, [[(1, 1), (4, 0)], [(7, 2)], [(3, 2)]]))

    def test_empty_v4_and_v6_tables(self):
        data = BUILD.build_sbg2([], [], [], block_size=1)
        self.assertEqual(decode_sbg2(data), ([], 0, []))

    def test_single_entry_block(self):
        data = BUILD.build_sbg2(["US"], [(0, 1)], [], block_size=4096)
        self.assertEqual(decode_sbg2(data), (["US"], 0, [[(0, 1)]]))

    def test_block_size_must_be_positive(self):
        with self.assertRaises(ValueError):
            BUILD.build_sbg2([], [], [], 0)
        with self.assertRaises(argparse.ArgumentTypeError):
            BUILD.positive_int("-1")


if __name__ == "__main__":
    unittest.main()
