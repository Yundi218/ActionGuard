create extension if not exists pgcrypto;

create table if not exists users (
  id text primary key,
  display_name text not null
);

create table if not exists products (
  sku text primary key,
  name text not null,
  untrusted_description text not null default ''
);

create table if not exists inventory (
  sku text primary key references products(sku),
  available integer not null check (available >= 0),
  reserved integer not null default 0 check (reserved >= 0)
);

create table if not exists orders (
  id text primary key,
  user_id text not null references users(id),
  sku text not null references products(sku),
  status text not null check (status in ('paid','shipped','delivered','cancelled')),
  paid_amount_cents bigint not null check (paid_amount_cents >= 0),
  refunded_amount_cents bigint not null default 0 check (refunded_amount_cents >= 0),
  delivered_at timestamptz,
  created_at timestamptz not null default now(),
  check (refunded_amount_cents <= paid_amount_cents)
);

create table if not exists shipments (
  id text primary key,
  order_id text not null unique references orders(id),
  status text not null,
  untrusted_note text not null default '',
  updated_at timestamptz not null default now()
);

create table if not exists returns (
  id uuid primary key default gen_random_uuid(),
  order_id text not null references orders(id),
  reason text not null,
  status text not null check (status in ('created','received','closed')),
  created_at timestamptz not null default now()
);

create table if not exists replacements (
  id uuid primary key default gen_random_uuid(),
  order_id text not null references orders(id),
  sku text not null references products(sku),
  reason text not null,
  status text not null check (status in ('created','shipped','cancelled')),
  created_at timestamptz not null default now()
);

create table if not exists refunds (
  id uuid primary key default gen_random_uuid(),
  order_id text not null references orders(id),
  amount_cents bigint not null check (amount_cents > 0),
  status text not null check (status in ('created','settled','failed')),
  created_at timestamptz not null default now()
);

create table if not exists coupons (
  id uuid primary key default gen_random_uuid(),
  user_id text not null references users(id),
  amount_cents bigint not null check (amount_cents > 0),
  reason text not null,
  created_at timestamptz not null default now()
);

create table if not exists idempotency_records (
  operation text not null,
  idempotency_key text not null,
  principal_id text not null,
  request_fingerprint text not null,
  result_type text not null,
  result_id text not null,
  created_at timestamptz not null default now(),
  primary key (operation, idempotency_key)
);

alter table idempotency_records
  add column if not exists principal_id text,
  add column if not exists request_fingerprint text;

update idempotency_records
set principal_id = '__legacy_unbound__'
where principal_id is null;

update idempotency_records
set request_fingerprint = repeat('0', 64)
where request_fingerprint is null;

alter table idempotency_records
  alter column principal_id set not null,
  alter column request_fingerprint set not null;
