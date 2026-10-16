#!/usr/bin/env python3
import sys
import browser_runtime as r

original_base = r.BASE
r.BASE = original_base / 'r1-runtime'
r.STATIC = original_base / 'frontend-r1/Frontends/dist'
r.OUT = r.OUT / 'r1/runtime'
r.PORTS = {'precision-cloud': 19523, 'precision-edge': 19524}
r.DIRECTORIES = {'precision-cloud': 'cloud-precision', 'precision-edge': 'edge-precision'}
r.BASE.mkdir(parents=True, exist_ok=True)
r.OUT.mkdir(parents=True, exist_ok=True)
if sys.argv[1] == 'seed':
    r.seed()
else:
    r.start(sys.argv[1])
