#!/usr/bin/env python3
"""Retain all empty-surface geometry while reporting every overflow assertion."""
import importlib.util
import pathlib
import sys

path = pathlib.Path(__file__).with_name('browser_review.py')
spec = importlib.util.spec_from_file_location('independent_browser_review', path)
review = importlib.util.module_from_spec(spec)
spec.loader.exec_module(review)
name = sys.argv[1]
report = review.OUT / name
report.mkdir()
env = {'SF_PHASE4_RUNTIME': str(review.BASE), 'SF_PHASE4_EVIDENCE': str(review.BASE / name), 'SF_PHASE4_CLOUD': 'http://127.0.0.1:19513', 'SF_PHASE4_EDGE': 'http://127.0.0.1:19514', 'SF_PHASE4_CASES': 'extra', 'SF_PHASE4_DATA_SUFFIX': 'precision', 'SF_PHASE4_REPORT_DIR': str(report)}
command = [str(review.NODE), str(review.WS / 'Frontends/node_modules/@playwright/test/cli.js'), 'test', '-c', 'playwright.phase4.config.ts', 'phase4-independent-extra.spec.ts', '--grep', 'independent empty queries']
raise SystemExit(review.run(name, command, env))
