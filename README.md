# torotator

SOCKS5 proxy that balances across several Tor sessions.

This project may be used to create one or more Tor sessions, providing access
to the Tor network as a sort of load balancer using HAProxy. Each Tor instance
is rotated after a certain amount of time, and each Tor circuit is re-routed
periodically as well.
