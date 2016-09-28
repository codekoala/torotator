# torotator

HTTP proxy that balances across several Tor sessions.

This project may be used to create one or more Tor sessions, providing access
to the Tor network as an HTTP proxy using privoxy. There will be one privoxy
instance per Tor session. Each privoxy instance is made available behind a
single instance of HAproxy, which is what you would configure your actual
applications to use.

Each Tor+Privoxy pair is rotated after a certain amount of time, and each Tor
session's circuit is routed periodically as well.
