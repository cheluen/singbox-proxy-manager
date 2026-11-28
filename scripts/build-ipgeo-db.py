#!/usr/bin/env python3
"""将 sapics/ip-location-db 编译为分块嵌入式 IP 地区表。"""

import argparse
import pathlib
import struct
import subprocess
import tempfile
import zlib


DEFAULT_V4_URL = (
    "https://github.com/sapics/ip-location-db/releases/download/latest/"
    "user-country-ipv4-num.csv"
)
DEFAULT_V6_URL = (
    "https://github.com/sapics/ip-location-db/releases/download/latest/"
    "user-country-ipv6-num.csv"
)


def encode_uvarint(value: int) -> bytes:
    if value < 0:
        raise ValueError(f"negative uvarint: {value}")
    out = bytearray()
    while value >= 0x80:
        out.append((value & 0x7F) | 0x80)
        value >>= 7
    out.append(value)
    return bytes(out)


def positive_int(value: str) -> int:
    try:
        number = int(value)
    except ValueError as error:
        raise argparse.ArgumentTypeError("must be an integer") from error
    if number <= 0:
        raise argparse.ArgumentTypeError("must be positive")
    return number


def parse_ranges(csv_path: pathlib.Path, shift: int, max_key: int) -> list[tuple[int, int, str]]:
    items: list[tuple[int, int, str]] = []
    with csv_path.open("r", encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            parts = line.split(",")
            if len(parts) < 3:
                continue
            cc = parts[2].strip().upper()
            if len(cc) != 2 or not cc.isascii() or not cc.isalpha():
                continue
            start = int(parts[0]) >> shift
            end = int(parts[1]) >> shift
            if start > max_key:
                continue
            if end > max_key:
                end = max_key
            items.append((start, end, cc))
    items.sort(key=lambda item: (item[0], item[1]))
    merged: list[tuple[int, int, str]] = []
    for start, end, cc in items:
        if merged:
            prev_start, prev_end, prev_cc = merged[-1]
            if prev_cc == cc and prev_end + 1 >= start:
                merged[-1] = (prev_start, max(prev_end, end), cc)
                continue
            if start <= prev_end:
                start = prev_end + 1
                if start > end:
                    continue
        merged.append((start, end, cc))
    return merged


def build_transitions(
    ranges: list[tuple[int, int, str]], country_to_idx: dict[str, int], max_key: int
) -> list[tuple[int, int]]:
    transitions: list[tuple[int, int]] = []
    cur_idx = 0
    for start, end, cc in ranges:
        idx = country_to_idx[cc]
        if idx != cur_idx:
            if transitions and transitions[-1][0] == start:
                transitions[-1] = (start, idx)
            else:
                transitions.append((start, idx))
            cur_idx = idx
        if end < max_key:
            sentinel = end + 1
            transitions.append((sentinel, 0))
            cur_idx = 0
    # 合并相邻的同值段，避免被后续紧邻区间覆盖时留出冗余 0 哨兵
    deduped: list[tuple[int, int]] = []
    for key, idx in transitions:
        if deduped and deduped[-1][0] == key:
            deduped[-1] = (key, idx)
        elif not deduped or deduped[-1][1] != idx:
            if not deduped and idx == 0:
                continue
            deduped.append((key, idx))
    return deduped


def download(url: str, dest: pathlib.Path) -> None:
    subprocess.run(
        ["curl", "-fsSL", "--compressed", "--max-time", "120", url, "-o", str(dest)],
        check=True,
    )


def compress_block(transitions: list[tuple[int, int]]) -> bytes:
    raw = bytearray([transitions[0][1]])
    previous_key = transitions[0][0]
    for key, country_index in transitions[1:]:
        raw += encode_uvarint(key - previous_key)
        raw.append(country_index)
        previous_key = key
    compressor = zlib.compressobj(level=9, method=zlib.DEFLATED, wbits=-15)
    return compressor.compress(bytes(raw)) + compressor.flush()


def build_sbg2(
    countries: list[str],
    v4_transitions: list[tuple[int, int]],
    v6_transitions: list[tuple[int, int]],
    block_size: int,
) -> bytes:
    if block_size <= 0:
        raise ValueError("block_size must be positive")
    if len(countries) > 255:
        raise ValueError(f"too many country codes: {len(countries)}")

    v4_blocks = [
        (chunk[0][0], compress_block(chunk))
        for start in range(0, len(v4_transitions), block_size)
        if (chunk := v4_transitions[start : start + block_size])
    ]
    v6_blocks = [
        (chunk[0][0], compress_block(chunk))
        for start in range(0, len(v6_transitions), block_size)
        if (chunk := v6_transitions[start : start + block_size])
    ]
    if len(v4_blocks) > 0xFFFF or len(v6_blocks) > 0xFFFF:
        raise ValueError("too many compressed blocks")

    index_size = len(v4_blocks) * 8 + len(v6_blocks) * 12
    offset = 10 + len(countries) * 2 + index_size + 4
    indexes = bytearray()
    payload = bytearray()
    for first_key, compressed in v4_blocks:
        indexes += struct.pack(">II", first_key, offset)
        payload += compressed
        offset += len(compressed)
    for first_key, compressed in v6_blocks:
        indexes += struct.pack(">QI", first_key, offset)
        payload += compressed
        offset += len(compressed)
    if offset > 0xFFFFFFFF:
        raise ValueError("output is too large")

    output = bytearray(b"SBG2")
    output += struct.pack(">BBHH", len(countries), 0, len(v4_blocks), len(v6_blocks))
    for country in countries:
        encoded = country.encode("ascii")
        if len(encoded) != 2:
            raise ValueError(f"invalid country code: {country!r}")
        output += encoded
    output += indexes
    output += struct.pack(">I", offset)
    output += payload
    return bytes(output)


def main() -> None:
    parser = argparse.ArgumentParser(description="Build compact embedded IP-country DB")
    parser.add_argument(
        "--output",
        default="backend/services/ipgeo_data.bin",
        help="Output .bin file path",
    )
    parser.add_argument("--v4-csv", default="", help="Optional local IPv4 num CSV path")
    parser.add_argument("--v6-csv", default="", help="Optional local IPv6 num CSV path")
    parser.add_argument(
        "--block-size", type=positive_int, default=4096, help="Transitions per compressed block"
    )
    args = parser.parse_args()

    with tempfile.TemporaryDirectory(prefix="sbpm-ipgeo-") as tmp_dir:
        tmp = pathlib.Path(tmp_dir)
        v4_path = pathlib.Path(args.v4_csv) if args.v4_csv else tmp / "v4.csv"
        v6_path = pathlib.Path(args.v6_csv) if args.v6_csv else tmp / "v6.csv"
        if not args.v4_csv:
            download(DEFAULT_V4_URL, v4_path)
        if not args.v6_csv:
            download(DEFAULT_V6_URL, v6_path)

        v4_ranges = parse_ranges(v4_path, shift=8, max_key=(1 << 24) - 1)
        v6_ranges = parse_ranges(v6_path, shift=80, max_key=(1 << 48) - 1)

        countries = sorted({cc for _, _, cc in v4_ranges} | {cc for _, _, cc in v6_ranges})
        if len(countries) > 255:
            raise RuntimeError(f"too many country codes: {len(countries)}")
        country_to_idx = {cc: i + 1 for i, cc in enumerate(countries)}

        v4_trans = build_transitions(v4_ranges, country_to_idx, (1 << 24) - 1)
        v6_trans = build_transitions(v6_ranges, country_to_idx, (1 << 48) - 1)
        payload = build_sbg2(countries, v4_trans, v6_trans, args.block_size)
        out_path = pathlib.Path(args.output)
        out_path.parent.mkdir(parents=True, exist_ok=True)
        out_path.write_bytes(payload)

        v4_blocks = (len(v4_trans) + args.block_size - 1) // args.block_size
        v6_blocks = (len(v6_trans) + args.block_size - 1) // args.block_size
        sparse_index_bytes = v4_blocks * 8 + v6_blocks * 12
        print(
            f"Built {out_path}: countries={len(countries)}, "
            f"v4_blocks={v4_blocks}, v6_blocks={v6_blocks}, "
            f"sparse_index={sparse_index_bytes}B, output={len(payload)}B "
            f"({len(payload) / 1024:.1f}KB)"
        )


if __name__ == "__main__":
    main()
