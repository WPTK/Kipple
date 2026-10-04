Outgoing requests (feeds, article extraction, the image proxy) now reuse connections to the same host instead of opening a new one per request, which saves TLS handshakes on pages with many images.
