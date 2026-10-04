## Human

We need to decide how the PostgreSQL migration runner handles failures in
production. Alice Chen said the deploy pipeline on Kubernetes keeps timing out
when a migration holds a lock on the users table.

## Assistant

I recommend wrapping each migration in a transaction. Alice Chen decided to use pgx as
the driver, and the runner depends on pgx too. Bob Martinez joined the platform
team last week and works on the staging cluster. We decided to use pgx as
the driver because it has connection pooling built in, and Redis stays the
cache for session tokens. The migrations table is updated only after a commit,
so a failed migration has no effect. Bob Martinez maintains the Terraform
modules for the staging cluster and will review the rollout plan on Monday.

## Human

What about tests? The integration tests run against Docker containers in CI,
and GitHub Actions caches the Go module downloads between runs.

## Assistant

Each migration gets an up and a down file, and the CI job applies every up
migration, then every down migration, against a fresh PostgreSQL container.
We should also benchmark the runner with ten thousand migrations, because the
version table scan is linear today. Alice Chen agreed to own the benchmark and
to report the numbers in the weekly architecture review.
