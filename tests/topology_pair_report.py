import json
import random
import statistics
from pathlib import Path
folder=Path('/reports')
on=[json.loads((folder/f'topology-throughput-{i}-on.json').read_text()) for i in range(1,6)]
off=[json.loads((folder/f'topology-throughput-{i}-off.json').read_text()) for i in range(1,6)]
ratios=[a['mbps']/b['mbps'] for a,b in zip(on,off)]
degradation=100*(1-statistics.median(ratios))
random.seed(1)
bootstrap=sorted(100*(1-statistics.median(random.choices(ratios,k=5))) for _ in range(10000))
upper=bootstrap[9749]
cv=lambda values:statistics.stdev(values)/statistics.mean(values)
noise=max(cv([r['mbps'] for r in on]),cv([r['mbps'] for r in off]))
heap_delta=statistics.median(a['worker_heap_bytes']-b['worker_heap_bytes'] for a,b in zip(on,off))
report={'paired_median_worker_heap_delta_bytes':heap_delta,'pairs':list(zip(off,on)),'paired_on_off_ratios':ratios,'median_degradation_percent':degradation,'bootstrap_95_upper_degradation_percent':upper,'max_coefficient_of_variation':noise,'passed':degradation<=3 and upper<=3 and noise<=.05 and heap_delta<=64*1024*1024,'limits':'Shared host workloads may introduce noise; failed/inconclusive is not a pass. Every measured pair is retained.'}
(folder/'topology-paired-throughput.json').write_text(json.dumps(report,indent=2))
print(json.dumps({k:v for k,v in report.items() if k!='pairs'}),flush=True)
