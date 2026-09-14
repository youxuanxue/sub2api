#!/usr/bin/env python3
"""Summarize upstream changes touching TokenKey SSOT owner areas."""
import argparse, subprocess
from collections import defaultdict
HINTS={"protocol":("protocolrouter","protocol-routing","protocol_endpoint_capabilities"),"candidate":("candidate_","candidate-eligibility","scheduler"),"pricing":("pricing","model-pricing","model-surface","servable"),"catalog":("catalog","manifest","model_mapping"),"gateway":("gateway","handler")}
def main():
    ap=argparse.ArgumentParser(); ap.add_argument('--base',default='origin/main'); ap.add_argument('--upstream',default='upstream/main'); a=ap.parse_args(); groups=defaultdict(int)
    try: raw=subprocess.check_output(['git','diff','--name-only',f'{a.base}...{a.upstream}'],text=True)
    except subprocess.CalledProcessError: print('upstream SSOT impact: unavailable'); return 0
    for p in raw.splitlines():
        low=p.lower(); hit=False
        for owner,hs in HINTS.items():
            if any(h in low for h in hs): groups[owner]+=1; hit=True
        if not hit: groups['other']+=1
    print(f"upstream SSOT impact: {sum(groups.values())} changed paths")
    for owner in sorted(groups): print(f"  {owner}: {groups[owner]} paths")
if __name__=='__main__': main()
