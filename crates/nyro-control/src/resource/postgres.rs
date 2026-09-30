use crate::Error;
use sqlx::{
    ConnectOptions,
    postgres::{PgConnectOptions, PgSslMode},
};
use std::{net::IpAddr, str::FromStr};
pub(crate) fn options(url: &str) -> std::result::Result<PgConnectOptions, Error> {
    if !(url.starts_with("postgres://") || url.starts_with("postgresql://")) || url.contains('#') {
        return Err(Error::Storage);
    }
    let destination = url
        .split_once("://")
        .ok_or(Error::Storage)?
        .1
        .split('?')
        .next()
        .ok_or(Error::Storage)?;
    let (authority, database) = destination.split_once('/').ok_or(Error::Storage)?;
    let host = authority.rsplit('@').next().ok_or(Error::Storage)?;
    if host.is_empty() || host.starts_with(':') || database.trim_matches('/').is_empty() {
        return Err(Error::Storage);
    }
    // SQLx logs unrecognized URL query values. Reject them before its parser sees them.
    let mut explicit_tls = false;
    if let Some((_, query)) = url.split_once('?') {
        for pair in query.split('&') {
            let (key, _) = pair.split_once('=').ok_or(Error::Storage)?;
            match key {
                "sslmode" | "ssl-mode" if !explicit_tls => explicit_tls = true,
                "sslrootcert" | "ssl-root-cert" | "ssl-ca" | "sslcert" | "ssl-cert" | "sslkey"
                | "ssl-key" => {}
                _ => return Err(Error::Storage),
            }
        }
    }
    // SQLx may also log malformed pgpass records (including their contents).
    // Suppression is scoped to this synchronous parser, never process-global.
    let mut options =
        tracing::subscriber::with_default(tracing::subscriber::NoSubscriber::default(), || {
            PgConnectOptions::from_str(url)
        })
        .map_err(|_| Error::Storage)?
        .disable_statement_logging();
    if !explicit_tls {
        options = options.ssl_mode(PgSslMode::VerifyFull);
    }
    if options.get_database().is_none_or(str::is_empty) || options.get_socket().is_some() {
        return Err(Error::Storage);
    }
    match options.get_ssl_mode() {
        PgSslMode::Disable
            if options.get_host() == "localhost"
                || options
                    .get_host()
                    .trim_matches(['[', ']'])
                    .parse::<IpAddr>()
                    .is_ok_and(|ip| ip.is_loopback()) => {}
        PgSslMode::Require | PgSslMode::VerifyCa | PgSslMode::VerifyFull => {}
        _ => return Err(Error::Storage),
    }
    Ok(options.application_name("nyro-control").options([
        ("statement_timeout", "3000"),
        ("lock_timeout", "1000"),
        ("synchronous_commit", "on"),
        ("search_path", "pg_catalog"),
    ]))
}
