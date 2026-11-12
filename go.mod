module ledgerline

go 1.24.0

replace golang.org/x/crypto => github.com/golang/crypto v0.45.0

replace golang.org/x/sys => github.com/golang/sys v0.38.0

replace golang.org/x/term => github.com/golang/term v0.37.0

replace golang.org/x/net => github.com/golang/net v0.47.0

replace golang.org/x/text => github.com/golang/text v0.31.0

replace golang.org/x/mod => github.com/golang/mod v0.30.0

replace golang.org/x/tools => github.com/golang/tools v0.39.0

replace golang.org/x/sync => github.com/golang/sync v0.18.0

require (
	github.com/bodgit/sevenzip v1.6.0
	github.com/microsoft/go-mssqldb v1.8.0
	github.com/pkg/sftp v1.13.10
	golang.org/x/crypto v0.45.0
)

require (
	github.com/andybalholm/brotli v1.1.1 // indirect
	github.com/bodgit/plumbing v1.3.0 // indirect
	github.com/bodgit/windows v1.0.1 // indirect
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/errwrap v1.0.0 // indirect
	github.com/hashicorp/go-multierror v1.1.1 // indirect
	github.com/hashicorp/golang-lru/v2 v2.0.7 // indirect
	github.com/klauspost/compress v1.17.9 // indirect
	github.com/kr/fs v0.1.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.21 // indirect
	github.com/ulikunitz/xz v0.5.12 // indirect
	go4.org v0.0.0-20200411211856-f5505b9728dd // indirect
	golang.org/x/sys v0.38.0 // indirect
	golang.org/x/text v0.31.0 // indirect
)

replace (
	gopkg.in/yaml.v3 v3.0.0-20200313102051-9f266ea9e77c => ./third_party/yaml.v3
	gopkg.in/yaml.v3 v3.0.1 => ./third_party/yaml.v3
)

replace go4.org v0.0.0-20200411211856-f5505b9728dd => ./third_party/go4

replace gopkg.in/check.v1 v0.0.0-20161208181325-20d25e280405 => ./third_party/check.v1
