# blkstorage

This is a copy of `github.com/hyperledger/fabric@v2.1.1`
block storage packages (`common/ledger/blkstorage` and
`common/ledger/blkstorage/fsblkstorage`). This avoids depending on the
full Fabric module which has unresolvable CVEs and broken Go module paths.
