$ErrorActionPreference = 'Stop'

$composeFile = Join-Path $PSScriptRoot '..\deployments\compose\docker-compose.yaml'
docker compose -f $composeFile up -d --wait postgres redis

$env:TEST_DATABASE_URL = 'postgres://projectrebound:projectrebound_dev@127.0.0.1:5432/projectrebound?sslmode=disable'
try {
    go test ./internal/database ./internal/auth ./internal/admin ./internal/gameserver -run 'Test(Migrator|AuthenticationLifecycle|AdminPlayerLifecycle|GameServerRegistry)AgainstPostgreSQL' -count=1
}
finally {
    Remove-Item Env:\TEST_DATABASE_URL -ErrorAction SilentlyContinue
    docker compose -f $composeFile stop postgres redis
}
