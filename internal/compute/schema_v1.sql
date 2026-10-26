CREATE TABLE IF NOT EXISTS compute_installation_state(
 installation text PRIMARY KEY,owner text NOT NULL DEFAULT '',epoch bigint NOT NULL DEFAULT 0,
 lease_until timestamptz NOT NULL DEFAULT '-infinity',high_day date NOT NULL DEFAULT ((clock_timestamp()AT TIME ZONE'UTC')::date),
 uncertain_until timestamptz NOT NULL DEFAULT '-infinity');
 CREATE TABLE IF NOT EXISTS compute_budget_window(
 installation text NOT NULL,window_id text NOT NULL,cpu_ms bigint NOT NULL DEFAULT 0,network_bytes bigint NOT NULL DEFAULT 0,
 PRIMARY KEY(installation,window_id),CHECK(cpu_ms>=0),CHECK(network_bytes>=0));
 CREATE TABLE IF NOT EXISTS compute_worker(
 installation text NOT NULL,id text NOT NULL,session text NOT NULL,approved boolean NOT NULL DEFAULT false,
 seen_at timestamptz NOT NULL DEFAULT clock_timestamp(),revoked boolean NOT NULL DEFAULT false,key_hash text NOT NULL DEFAULT '',
 PRIMARY KEY(installation,id));
 ALTER TABLE compute_worker ADD COLUMN IF NOT EXISTS key_hash text NOT NULL DEFAULT '';
 ALTER TABLE compute_worker ADD COLUMN IF NOT EXISTS registered_at timestamptz NOT NULL DEFAULT '-infinity';
 ALTER TABLE compute_worker ADD COLUMN IF NOT EXISTS invalid_since timestamptz NOT NULL DEFAULT '-infinity';
 ALTER TABLE compute_worker ADD COLUMN IF NOT EXISTS invalid_count integer NOT NULL DEFAULT 0;
 CREATE TABLE IF NOT EXISTS compute_task(
 installation text NOT NULL,id text NOT NULL,epoch bigint NOT NULL,snapshot jsonb NOT NULL,snapshot_digest text NOT NULL,
 candidate_first integer NOT NULL,candidate_count integer NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 expires_at timestamptz NOT NULL DEFAULT clock_timestamp()+interval '8 seconds',PRIMARY KEY(installation,id),
 UNIQUE(installation,epoch,snapshot_digest,candidate_first));
 CREATE TABLE IF NOT EXISTS compute_attempt(
 installation text NOT NULL,id text NOT NULL,task text NOT NULL,worker text NOT NULL,session text NOT NULL,
 request_id text NOT NULL,epoch bigint NOT NULL,token_hash text NOT NULL,state text NOT NULL,
 expires_at timestamptz NOT NULL,payload jsonb NOT NULL,reserved_cpu_ms bigint NOT NULL,
 result_digest text,result jsonb,PRIMARY KEY(installation,id),UNIQUE(installation,worker,request_id),
 CHECK(state IN ('leased','running','completed','expired','cancelled','failed')));
 ALTER TABLE compute_attempt ADD COLUMN IF NOT EXISTS reserved_at timestamptz NOT NULL DEFAULT clock_timestamp();
 ALTER TABLE compute_attempt ADD COLUMN IF NOT EXISTS publication_checked boolean NOT NULL DEFAULT false;
 CREATE TABLE IF NOT EXISTS compute_empty_request(installation text NOT NULL,worker text NOT NULL,request_id text NOT NULL,created_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(installation,worker,request_id));
 CREATE UNIQUE INDEX IF NOT EXISTS compute_live_task ON compute_attempt(installation,task) WHERE state IN ('leased','running');
 CREATE UNIQUE INDEX IF NOT EXISTS compute_live_worker ON compute_attempt(installation,worker) WHERE state IN ('leased','running');
 CREATE TABLE IF NOT EXISTS compute_publication(
 installation text NOT NULL,snapshot_digest text NOT NULL,sequence bigserial,epoch bigint NOT NULL,payload jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(installation,snapshot_digest));
 CREATE TABLE IF NOT EXISTS compute_audit(
 installation text NOT NULL,id bigserial,actor text NOT NULL,action text NOT NULL,subject text NOT NULL,
 detail jsonb NOT NULL DEFAULT '{}',created_at timestamptz NOT NULL DEFAULT clock_timestamp(),PRIMARY KEY(installation,id));
