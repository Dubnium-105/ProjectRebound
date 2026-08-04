$ErrorActionPreference = 'Stop'

$composeFile = Join-Path $PSScriptRoot '..\deployments\compose\docker-compose.yaml'
docker compose -f $composeFile up -d --wait postgres redis

$env:TEST_DATABASE_URL = 'postgres://projectrebound:projectrebound_dev@127.0.0.1:5432/projectrebound?sslmode=disable'
$env:TEST_REDIS_ADDRESS = '127.0.0.1:6379'
try {
    go test ./internal/database ./internal/auth ./internal/admin ./internal/gameserver ./internal/p2proom ./internal/connection ./internal/relayregistry ./internal/vnt -run 'Test(Migrator|AuthenticationLifecycle|AdminPlayerLifecycle|AdministratorDrainsAndRevokesVNTNode|GameServerRegistry|P2PRoomLifecycle|VNTRoomLifecycle|ConnectionLifecycle|RelayRegistryLifecycle|RelayMigrationLifecycle|NodeDirectoryPagination|NodeRetirementRevokesCredentials|CredentialRotationOverlap|OwnerRecoveryAndRetirement|EnrollmentEnforcesOwnerQuota)AgainstPostgreSQL|TestRedisVNTLimitStoreIsAtomicAndExpires' -count=1
}
finally {
    Remove-Item Env:\TEST_DATABASE_URL -ErrorAction SilentlyContinue
    Remove-Item Env:\TEST_REDIS_ADDRESS -ErrorAction SilentlyContinue
    docker compose -f $composeFile stop postgres redis
}
