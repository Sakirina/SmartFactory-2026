#!/usr/bin/env python3
"""Check declared versions, image digests, Compose images and installed build tools."""
import argparse
import json
from pathlib import Path
import sys

from toolchain import ROOT, configuration_errors, load_manifest, verify_tools


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--configuration-only', action='store_true')
    parser.add_argument('--root', type=Path, default=ROOT)
    parser.add_argument('--output', type=Path)
    args = parser.parse_args()
    errors = configuration_errors(args.root)
    observed = {}
    if not args.configuration_only:
        observed, tool_errors = verify_tools(args.root)
        errors.extend(tool_errors)
    result = {'schema_version': 1, 'ok': not errors, 'observed': observed, 'selected': load_manifest(args.root)['tools'], 'errors': errors}
    if args.output:
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return 1 if errors else 0


if __name__ == '__main__':
    sys.exit(main())
