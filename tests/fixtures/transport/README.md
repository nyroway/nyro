# Local transport TLS fixture

`cert.pem` and `key.pem` are a public, test-only self-signed server certificate
and private key for localhost / 127.0.0.1. Never use this key for a deployment.
The certificate expires in September 2036. The smoke test supplies
`SSL_CERT_FILE` only to its Nyro child process; it does not alter the system
trust store or disable TLS verification.

Regenerate from the repository root when needed:

```sh
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout tests/fixtures/transport/key.pem \
  -out tests/fixtures/transport/cert.pem -days 3650 -subj /CN=localhost \
  -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1' \
  -addext 'basicConstraints=critical,CA:FALSE' \
  -addext 'keyUsage=critical,digitalSignature,keyEncipherment' \
  -addext 'extendedKeyUsage=serverAuth'
```

Use an end-entity certificate (`CA:FALSE`): rustls rejects CA certificates
presented as server certificates even when explicitly trusted.
