create extension if not exists vector;

alter table products
  add column if not exists category text;

alter table products
  alter column category set default 'general';

update products
set category = 'general'
where category is null;

alter table products
  alter column category set not null;

create table if not exists policy_documents (
  policy_id text not null,
  version text not null,
  source_name text not null,
  effective_from timestamptz not null,
  effective_to timestamptz not null,
  region text not null,
  product_category text not null,
  risk_level text not null,
  max_coupon_cents bigint,
  content_sha256 text not null,
  imported_at timestamptz not null default now(),
  primary key (policy_id, version)
);

create table if not exists policy_chunks (
  id text primary key,
  policy_id text not null,
  version text not null,
  section text not null,
  content text not null,
  start_offset integer not null,
  end_offset integer not null,
  embedding vector(1536) not null,
  search_vector tsvector generated always as (to_tsvector('english', content)) stored,
  foreign key (policy_id, version)
    references policy_documents(policy_id, version) on delete cascade
);

create index if not exists policy_documents_applicability_idx
  on policy_documents (region, product_category, effective_from, effective_to);

create index if not exists policy_chunks_search_vector_idx
  on policy_chunks using gin (search_vector);

create index if not exists policy_chunks_embedding_idx
  on policy_chunks using hnsw (embedding vector_cosine_ops);

create table if not exists sessions (
  id text primary key,
  user_id text not null references users(id),
  region text not null,
  created_at timestamptz not null default now()
);

create table if not exists runs (
  id text primary key,
  session_id text not null references sessions(id) on delete cascade,
  status text not null check (status in (
    'planning',
    'needs_input',
    'ready',
    'running',
    'waiting_runtime',
    'succeeded',
    'failed'
  )),
  goal text not null default '',
  failure_code text not null default '',
  failure_detail text not null default '',
  result_json jsonb not null default '{}'::jsonb,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now()
);

create table if not exists messages (
  id bigserial primary key,
  session_id text not null references sessions(id) on delete cascade,
  run_id text not null references runs(id) on delete cascade,
  role text not null,
  content text not null,
  created_at timestamptz not null default now()
);

create table if not exists plans (
  run_id text not null references runs(id) on delete cascade,
  plan_version integer not null,
  plan_json jsonb not null,
  evidence_json jsonb not null,
  verification_json jsonb not null,
  created_at timestamptz not null default now(),
  primary key (run_id, plan_version)
);
