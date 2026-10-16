"""Operate only controls present in a fresh, saved Playwright snapshot."""
import importlib.util,re
from pathlib import Path
p=Path(__file__).with_name('browser_review_v2.py');s=importlib.util.spec_from_file_location('b',p);b=importlib.util.module_from_spec(s);s.loader.exec_module(b)
def refs(mode,role,label):
 raw=b.cli(mode,['snapshot'],quiet=True)
 matches=re.findall(r'- '+re.escape(role)+r' "(?:loading )?'+re.escape(label)+r'"[^\n]*?\[ref=([^\]]+)\]',raw)
 if not matches:raise RuntimeError('No observed '+role+' named '+label)
 return matches

def click(mode,label,role='button',last=False):
 values=refs(mode,role,label)
 if len(values)!=1 and not last:raise RuntimeError('Ambiguous '+role+' '+label+': '+str(values))
 return b.cli(mode,['click',values[-1] if last else values[0]],quiet=True)

def fill(mode,label,value):
 values=refs(mode,'textbox',label)
 if len(values)!=1:raise RuntimeError('Ambiguous textbox '+label)
 return b.cli(mode,['fill',values[0],str(value)],quiet=True)
