"""Generate the provisioned Grafana dashboards, without runtime credentials."""
import json
from pathlib import Path

ROOT=Path(__file__).resolve().parents[1]/'observability/grafana/dashboards/json'
ROOT.mkdir(parents=True,exist_ok=True)
BOARDS={
 'overview':('Overview',[
  ('Active TCP connections','proxy_connections_active','short'),
  ('UDP associations','proxy_udp_associations_active','short'),
  ('Ready routes','sum(proxy_paths_ready)','short'),
  ('Live payload traffic, excludes handshake','sum by(direction)(rate(proxy_payload_bytes_total[2m])) * 8','bps'),
  ('Connection attempts','sum by(core)(rate(proxy_route_connections_total[2m]))','ops'),
  ('Connection failures','sum(rate(proxy_route_failures_total[2m]))','ops')]),
 'profiles':('Profiles and policies',[
  ('Connect latency EWMA','proxy_route_rtt_seconds','s'),
  ('Connections by profile','rate(proxy_route_connections_total[2m])','ops'),
  ('Failures by profile','rate(proxy_route_failures_total[2m])','ops'),
  ('Live payload by profile','rate(proxy_traffic_bytes_total[2m])*8','bps'),
  ('Cold catalog size','proxy_catalog_profiles','short'),
  ('Active test traffic','rate(proxy_probe_bytes_total[5m])','Bps'),
  ('Daily test budget remaining','proxy_probe_budget_remaining_bytes','bytes')]),
 'cores':('Core lifecycle',[
  ('Core readiness','proxy_core_ready','bool'),
  ('Restarts in 10 minutes','increase(proxy_core_restarts_total[10m])','short'),
  ('Managed processes including retiring sessions','proxy_engine_processes','short'),
  ('Core restarts','proxy_core_restarts_total','short'),
  ('Core memory','proxy_core_resident_memory_bytes','bytes'),
  ('Core file descriptors','proxy_core_open_fds','short'),
  ('Core CPU usage','rate(proxy_core_cpu_seconds_total[2m])','cores'),
  ('Worker available','up{job="workers"}','bool'),
  ('Offline lease remaining','proxy_offline_lease_until_seconds-time()','s')]),
 'workers':('Workers and recovery',[
  ('Metrics availability','up','bool'),
  ('Heartbeat age','time()-relayweave_worker_last_seen_timestamp_seconds','s'),
  ('Applied generation','relayweave_worker_applied_generation','short'),
  ('Lease remaining','proxy_offline_lease_until_seconds-time()','s'),
  ('Database available','relayweave_database_up','bool'),
  ('Certificate lifetime remaining','proxy_certificate_expiry_timestamp_seconds-time()','s'),
  ('Worker heap','go_memstats_heap_alloc_bytes','bytes'),
  ('Worker RSS','process_resident_memory_bytes','bytes'),
  ('State disk free','proxy_state_disk_free_bytes','bytes'),
  ('Encrypted catalog objects','relayweave_objects','short')]),
 'subscriptions':('Subscriptions and control',[
  ('Last successful refresh age','time()-proxy_subscription_last_success_timestamp_seconds','s'),
  ('Sources','relayweave_objects{kind="sources"}','short'),
  ('Profiles','relayweave_objects{kind="profiles"}','short'),
  ('Pools and policies','relayweave_objects{kind=~"pools|policies"}','short'),
  ('Listeners','relayweave_objects{kind="listeners"}','short'),
  ('Control health','up{job="control"}','bool')]),
 'udp-logs':('UDP and logs',[
  ('UDP associations','proxy_udp_associations_active','short'),
  ('UDP packets','rate(proxy_udp_packets_total[2m])','pps'),
  ('UDP drops','rate(proxy_udp_drops_total[2m])','pps'),
  ('UDP packet counter','proxy_udp_packets_total','short')])
}
for slug,(title,queries) in BOARDS.items():
 panels=[]
 for i,(label,query,unit) in enumerate(queries):
  panels.append({'id':i+1,'title':label,'type':'timeseries','gridPos':{'x':(i%2)*12,'y':(i//2)*8,'w':12,'h':8},'datasource':{'type':'prometheus','uid':'relayweave-prometheus'},'targets':[{'expr':query,'refId':'A','legendFormat':'{{route}}{{core}}{{worker}}{{direction}}{{job}}{{kind}}'}],'fieldConfig':{'defaults':{'unit':unit,'color':{'mode':'palette-classic'}},'overrides':[]},'options':{'legend':{'displayMode':'table','placement':'bottom'},'tooltip':{'mode':'multi'}}})
 if slug=='udp-logs':
  panels.append({'id':99,'title':'Structured application logs (credentials and destinations omitted)','type':'logs','gridPos':{'x':0,'y':16,'w':24,'h':12},'datasource':{'type':'loki','uid':'relayweave-loki'},'targets':[{'expr':'{app="relayweave"}','refId':'A'}],'options':{'showTime':True,'sortOrder':'Descending','wrapLogMessage':True}})
 dashboard={'uid':'rw-'+slug,'title':'RelayWeave / '+title,'tags':['relayweave'],'timezone':'browser','schemaVersion':41,'version':1,'refresh':'15s','time':{'from':'now-1h','to':'now'},'panels':panels,'editable':False,'annotations':{'list':[]},'links':[{'title':'RelayWeave','url':'/','type':'link'}]}
 (ROOT/(slug+'.json')).write_text(json.dumps(dashboard,indent=2)+'\n',encoding='utf-8')
