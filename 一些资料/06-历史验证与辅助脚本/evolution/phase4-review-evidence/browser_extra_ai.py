#!/usr/bin/env python3
"""Use fresh model and application state for the additional deletion-poll test."""
import importlib.util
import pathlib

path = pathlib.Path(__file__).with_name('browser_runtime.py')
spec = importlib.util.spec_from_file_location('independent_browser_runtime', path)
runtime = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runtime)
runtime.PORTS['ai'] = 19515
runtime.DIRECTORIES['ai'] = 'ai-extra'
runtime.OUT = runtime.OUT / 'extra-ai-runtime'
runtime.OUT.mkdir()
runtime.start('ai')
