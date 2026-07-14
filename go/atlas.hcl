// atlas.hcl drives versioned schema migrations for the mysql/postgres SQL
// storage backend. sqlite is excluded — it stays on GORM AutoMigrate.
// Full workflow, rationale, and Makefile targets: see
// go/docs/schema/migrations.md. Short version: `src` points at a plain SQL
// file rendered from the GORM models (not atlas's `data "external_schema"`,
// which needs a paid Atlas distribution), so every command here runs on
// the free Atlas Community Edition docker image.

// mysql_dev_url / postgres_dev_url point at a scratch database atlas uses
// internally to compute diffs — never the real target. Defaults match both
// the throwaway docker containers a local dev spins up and CI's service
// containers (see migrations.md); override with `--var` if yours run on
// different ports.
variable "mysql_dev_url" {
  type    = string
  default = "mysql://root:pass@localhost:13306/dev"
}

variable "postgres_dev_url" {
  type    = string
  default = "postgres://postgres:pass@localhost:15432/dev?sslmode=disable"
}

env "mysql" {
  src = "file://schema/mysql.sql"
  dev = var.mysql_dev_url
  migration {
    dir = "file://migrations/mysql"
  }
  format {
    migrate {
      diff = "{{ sql . \"  \" }}"
    }
  }
}

env "postgres" {
  src = "file://schema/postgres.sql"
  dev = var.postgres_dev_url
  migration {
    dir = "file://migrations/postgres"
  }
  format {
    migrate {
      diff = "{{ sql . \"  \" }}"
    }
  }
}
