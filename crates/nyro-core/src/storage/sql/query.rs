use super::dialect::SqlDialect;

#[derive(Debug, Clone)]
pub struct Pagination {
    pub limit: i64,
    pub offset: i64,
}

impl Default for Pagination {
    fn default() -> Self {
        Self {
            limit: 50,
            offset: 0,
        }
    }
}

pub fn pagination_clause(
    dialect: SqlDialect,
    _pagination: &Pagination,
    next_bind_index: usize,
) -> String {
    match dialect {
        SqlDialect::Postgres => {
            format!(
                " LIMIT {} OFFSET {}",
                dialect.placeholder(next_bind_index),
                dialect.placeholder(next_bind_index + 1)
            )
        }
        SqlDialect::Sqlite | SqlDialect::Mysql => " LIMIT ? OFFSET ?".to_string(),
    }
}

pub fn now_expr(dialect: SqlDialect) -> &'static str {
    match dialect {
        SqlDialect::Sqlite => "datetime('now')",
        SqlDialect::Postgres => "CURRENT_TIMESTAMP",
        SqlDialect::Mysql => "NOW()",
    }
}
