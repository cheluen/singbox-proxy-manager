#!/usr/bin/env python3
"""将 sapics/ip-location-db (user-country, PDDL-1.0) 编译为极轻量嵌入式二进制地区表。

二进制格式（gzip 压缩后内嵌至 backend/services/ipgeo_data.bin.gz）：
  - Magic: 4 字节 "SBG1"
  - countryCount: uvarint (1..255，索引 0 保留表示未分配间隙)
  - countries: countryCount * 2 字节 ASCII 大写 ISO 3166-1 alpha-2 代码
  - v4Count: uvarint（IPv4 /24 边界表项数，按 start24 严格递增）
    每项: deltaStart24 (uvarint) + countryIdx (1 字节)
  - v6Count: uvarint（IPv6 /48 边界表项数，按 start48 严格递增）
    每项: deltaStart48 (uvarint) + countryIdx (1 字节)
"""

import argparse
import gzip
import pathlib
import subprocess
import tempfile


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


def main() -> None:
    parser = argparse.ArgumentParser(description="Build compact embedded IP-country DB")
    parser.add_argument(
        "--output",
        default="backend/services/ipgeo_data.bin.gz",
        help="Output .bin.gz file path",
    )
    parser.add_argument("--v4-csv", default="", help="Optional local IPv4 num CSV path")
    parser.add_argument("--v6-csv", default="", help="Optional local IPv6 num CSV path")
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

        payload = bytearray(b"SBG1")
        payload += encode_uvarint(len(countries))
        for cc in countries:
            payload += cc.encode("ascii")

        for trans in (v4_trans, v6_trans):
            payload += encode_uvarint(len(trans))
            prev = 0
            for key, idx in trans:
                payload += encode_uvarint(key - prev)
                payload.append(idx)
                prev = key

        out_path = pathlib.Path(args.output)
        out_path.parent.mkdir(parents=True, exist_ok=True)
        compressed = gzip.compress(bytes(payload), compresslevel=9, mtime=0)
        out_path.write_bytes(compressed)

        ram_mb = (len(v4_trans) * 4 + len(v6_trans) * 8) / (1024 * 1024)
        print(
            f"Built {out_path}: countries={len(countries)}, "
            f"v4_entries={len(v4_trans)}, v6_entries={len(v6_trans)}, "
            f"raw={len(payload)//1024}KB, gz={len(compressed)//1024}KB, "
            f"runtime_ram~{ram_mb:.2f}MB"
        )


if __name__ == "__main__":
    main()
