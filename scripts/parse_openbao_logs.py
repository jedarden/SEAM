#!/usr/bin/env python3
"""
OpenBao Service Log Parser

Extracts API read call entries from OpenBao service logs.
Supports multiple log formats commonly used by OpenBao/Vault.

Usage:
    python3 parse_openbao_logs.py <log-file-path> [--output <output-file>]
    python3 parse_openbao_logs.py --example  # Show example log formats

Acceptance Criteria:
    - Reads OpenBao service logs
    - Identifies and extracts API read call entries
    - Extracted data includes: timestamp, endpoint path, HTTP method
    - Handles the identified log format correctly
"""

import re
import json
import sys
import argparse
from datetime import datetime
from pathlib import Path
from typing import List, Dict, Optional, Iterator


class OpenBaoLogParser:
    """Parser for OpenBao service logs."""

    # Patterns for different log formats
    # Format 1: Standard text log format
    # Example: 2024-08-13T10:30:45.123Z [INFO]  core: handling request -> method=GET path=/v1/secret/data/test
    TEXT_PATTERN = re.compile(
        r'(?P<timestamp>\d{4}-\d{2}-\d{2}T[\d:.]+Z?)\s+\[\w+\]\s+.*?method=(?P<method>\w+)\s+path=(?P<path>[^\s]+)',
        re.IGNORECASE
    )

    # Format 2: Structured JSON log
    # Example: {"time":"2024-08-13T10:30:45.123Z","method":"GET","path":"/v1/secret/data/test"}
    # Will be handled by JSON parsing

    # Format 3: HTTP access log style
    # Example: 10.0.0.1 - - [13/Aug/2024:10:30:45 +0000] "GET /v1/secret/data/test HTTP/1.1" 200
    HTTP_PATTERN = re.compile(
        r'(?P<timestamp>\[[^]]+\])\s+"(?P<method>\w+)\s+(?P<path>/v1/[^\s]+)\s+HTTP',
        re.IGNORECASE
    )

    # Filter for read operations (GET methods only)
    READ_METHODS = {'GET'}

    def __init__(self, log_file: Path):
        """Initialize parser with log file path."""
        self.log_file = log_file

    def parse_line(self, line: str) -> Optional[Dict[str, str]]:
        """
        Parse a single log line and extract API call information.

        Returns:
            Dict with keys: timestamp, method, path (if line is a read API call)
            None: if line doesn't match expected format or is not a read call
        """
        if not line.strip():
            return None

        # Try JSON format first
        try:
            data = json.loads(line)
            if isinstance(data, dict):
                method = data.get('method', '')
                path = data.get('path', '')
                timestamp = data.get('time') or data.get('timestamp') or data.get('@timestamp')

                if method and path and timestamp:
                    if method.upper() in self.READ_METHODS and path.startswith('/v1/'):
                        return {
                            'timestamp': timestamp,
                            'method': method.upper(),
                            'path': path
                        }
        except json.JSONDecodeError:
            pass

        # Try standard text format
        match = self.TEXT_PATTERN.search(line)
        if match:
            method = match.group('method')
            path = match.group('path')
            timestamp = match.group('timestamp')

            if method.upper() in self.READ_METHODS and path.startswith('/v1/'):
                return {
                    'timestamp': timestamp,
                    'method': method.upper(),
                    'path': path
                }

        # Try HTTP access log format
        match = self.HTTP_PATTERN.search(line)
        if match:
            method = match.group('method')
            path = match.group('path')
            timestamp = match.group('timestamp').strip('[]')

            if method.upper() in self.READ_METHODS and path.startswith('/v1/'):
                return {
                    'timestamp': timestamp,
                    'method': method.upper(),
                    'path': path
                }

        return None

    def parse(self) -> Iterator[Dict[str, str]]:
        """
        Parse the entire log file and yield API read call entries.

        Yields:
            Dict with keys: timestamp, method, path
        """
        try:
            with open(self.log_file, 'r', encoding='utf-8', errors='replace') as f:
                for line in f:
                    result = self.parse_line(line)
                    if result:
                        yield result
        except FileNotFoundError:
            raise
        except Exception as e:
            raise RuntimeError(f"Error reading log file: {e}")


def format_output(calls: List[Dict[str, str]], format_type: str = 'text') -> str:
    """Format parsed API calls for output."""
    if format_type == 'json':
        return json.dumps(calls, indent=2)

    # Text format
    lines = [
        "OpenBao API Read Calls",
        "=" * 80,
        f"Total read calls found: {len(calls)}",
        "",
        "{:<30} {:<10} {}".format("Timestamp", "Method", "Path"),
        "-" * 80
    ]

    for call in calls:
        lines.append("{:<30} {:<10} {}".format(
            call['timestamp'][:30],  # Truncate long timestamps
            call['method'],
            call['path']
        ))

    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(
        description='Parse OpenBao service logs and extract API read calls',
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  %(prog)s /var/log/openbao/openbao.log
  %(prog)s /var/log/openbao/openbao.log --output results.json
  %(prog)s --example

Supported log formats:
  1. Standard text: 2024-08-13T10:30:45.123Z [INFO] ... method=GET path=/v1/secret/data/test
  2. JSON: {"time":"2024-08-13T10:30:45.123Z","method":"GET","path":"/v1/secret/data/test"}
  3. HTTP access: [13/Aug/2024:10:30:45 +0000] "GET /v1/secret/data/test HTTP/1.1" 200
        """
    )

    parser.add_argument(
        'log_file',
        nargs='?',
        type=Path,
        help='Path to OpenBao log file'
    )

    parser.add_argument(
        '--output', '-o',
        type=Path,
        help='Output file path (default: stdout)'
    )

    parser.add_argument(
        '--format', '-f',
        choices=['text', 'json'],
        default='text',
        help='Output format (default: text)'
    )

    parser.add_argument(
        '--example',
        action='store_true',
        help='Show example log formats and exit'
    )

    args = parser.parse_args()

    if args.example:
        print("=" * 80)
        print("OpenBao Log Format Examples")
        print("=" * 80)
        print()

        print("Format 1: Standard text format")
        print("-" * 40)
        print("2024-08-13T10:30:45.123Z [INFO]  core: handling request -> method=GET path=/v1/secret/data/seam/routes/test")
        print()

        print("Format 2: Structured JSON")
        print("-" * 40)
        print('{"time":"2024-08-13T10:30:45.123Z","level":"info","method":"GET","path":"/v1/secret/data/seam/routes/test"}')
        print()

        print("Format 3: HTTP access log style")
        print("-" * 40)
        print('10.0.0.1 - - [13/Aug/2024:10:30:45 +0000] "GET /v1/secret/data/seam/routes/test HTTP/1.1" 200')
        print()

        print("Note: The parser extracts read calls (GET method) to /v1/ paths")
        print("=" * 80)
        return

    if not args.log_file:
        parser.error("log_file is required (or use --example to see format examples)")

    if not args.log_file.exists():
        print(f"Error: Log file not found: {args.log_file}", file=sys.stderr)
        return 1

    try:
        # Parse the log file
        log_parser = OpenBaoLogParser(args.log_file)
        calls = list(log_parser.parse())

        # Format output
        output = format_output(calls, args.format)

        # Write output
        if args.output:
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(output, encoding='utf-8')
            print(f"Results written to: {args.output}")
            print(f"Total read calls found: {len(calls)}")
        else:
            print(output)

        return 0

    except Exception as e:
        print(f"Error: {e}", file=2)
        return 1


if __name__ == '__main__':
    exit(main())
