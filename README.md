# Rdv: Relay-assisted p2p connectivity

[![Go Reference](https://pkg.go.dev/badge/github.com/betamos/rdv.svg)](https://pkg.go.dev/github.com/betamos/rdv)

Rdv (from rendezvous) is a relay-assisted p2p connectivity library that quickly and reliably
establishes a TCP connection between two peers in any network topology,
with a relay fallback in the rare case where p2p isn't feasible. The library provides:

-   A client for dialing and accepting connections
-   A horizontally scalable http-based server, which acts as a rendezvous point and relay for clients
-   A CLI-tool for testing client and server

Rdv is designed to achieve p2p connectivity in real-world environments, without error-prone
monitoring of the network or using stateful and complex port-mapping protocols (like UPnP).
Clients use a small amount of resources while establishing connections, but after that there are
no idle cost, aside from the TCP connection itself.
[See how it works below](#how-does-it-work).

Rdv is built to support file transfers in [Payload](https://payload.app/).
Note that rdv is experimental and may change at any moment.
Always use immature software responsibly.
Feel free to use the issue tracker for questions and feedback.

## Why?

If you're writing a centralized app, you can get lower latency, higher bandwidth and reduced
operational costs, compared to sending p2p data through your servers.

If you're writing a decentralized or hybrid app, you can increase availability and QoS by having an
optional set of rdv servers, since relays are necessary in some topologies where p2p isn't feasible.
That said, rdv uses TCP, which isn't suitable for massive mesh-like networks with
hundreds of thousands of interconnected nodes.

You can also think of rdv as a <1000 LoC, minimal config alternative to WebRTC, but for non-realtime
use-cases and BYO authentication.

## Quick start

Install the rdv CLI on 2+ clients and the server: `go build -o rdv ./cmd` from the cloned repo.

```sh
# On your server
./rdv serve

# On client A
./rdv dial http://example.com:8080 MY_TOKEN  # Token is an arbitrary string, e.g. a UUID

# On client B
./rdv accept http://example.com:8080 MY_TOKEN  # Both clients need to provide the same token
```

On the clients, you should see something like:

```sh
CONNECTED: p2p 192.168.1.16:39841, 45ms
```

We got a local network TCP connection established in 45ms, great!

The `rdv` command connects stdin of A to stdout of B and vice versa, so you can now chat with your
peer. You can pipe files and stuff in and out of these commands (but you probably shouldn't,
since it's unencrypted):

```sh
./rdv dial ... < my_lastpass_vault.zip  # A publicly available file
./rdv accept ... > vault.zip  # Seriously, don't send anything sensitive
```

## Server setup

Simply add the rdv server to your exising http stack:

```go
func main() {
    server := rdv.NewServer(nil) // Config goes here, if you need any
    http.Handle("/rdv", server)
    go server.Serve()
    http.ListenAndServe(":8080", nil)
}
```

You can use TLS, auth tokens, cookies and any middleware you like, since this is just a regular
HTTP endpoint.

If you need multiple rdv servers, they are entirely independent and scale horizontally.
Just make sure that both the dialing and the accepting clients connect to the same relay.

### Beware of reverse proxies

To increase your chances of p2p connectivity, the rdv server needs to know the source
ipv4:port of clients, also known as the _observed address_.
In some environments, this is harder than it should be.

To check whether the rdv server gets the right address, go through the quick start guide above
(with the rdv server deployed to your real server environment),
and check the CLI output:

```sh
NOTICE: missing observed address
```

If you see that notice, you need to figure out who is meddling with your traffic, typically
a reverse proxy or a managed cloud provider.
Ask them to kindly
forward _both the source ip and port_ to your http server, by adding http headers such as
`X-Forwarded-For` and `X-Forwarded-Port` to inbound http requests.
Finally, you need to tell the rdv server to use these headers, by overriding the `ObservedAddrFunc`
in the `ServerConfig` struct.

## Client setup

Clients are stateless, so they're pretty easy to use:

```go
// On all clients
client := new(rdv.Client)

// Dialing
token := uuid.NewString()
conn, err := client.Dial("https://example.com/rdv", token)

// Accepting
conn, err := client.Accept("https://example.com/rdv", token)
```

### Signaling

While connecting is easy, you need to signal to the other peer (1) the address of the rdv server
(can be hardcoded if there's only one) and (2) the connection token. Typically, the dialer
generates a random token and signals your API of the connection attempt, which relays that
message to the destination peer, over e.g. a persistent websocket connection.

### Authentication

You need to decide how auth and identity should work in your application.
Perhaps peers have persistent public keys, or maybe you want the server to vouch for
peer identities through e.g. an auth token.

In either case, you should make sure connections are at least authenticated, and preferably
also end-to-end encrypted. You can use TLS from the standard library with client certificates,
for instance.

## How does it work?

Under the hood, rdv repackages a number of highly effective p2p techniques, notably
STUN, TURN and TCP simultaneous open, into a flow based on a single http request, which doubles as
a relay if needed:

```
  Alice                  Server                  Bob
    ┬                      ┬                      ┬
    │                      │                      |
    │            (server_addr, token)             |
    │ <~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~> │  (Signaling)
    │                      │                      |
    │ http/1.1 DIAL        │                      │
    ├────────────────────> │      http/1.1 ACCEPT │  Request
    │                      │ <────────────────────┤
    │                      │                      │
    │           101 Switching Protocols           │
    │ <────────────────────┼────────────────────> │  Response
    │                      │                      │
    │                  TCP dial                   │
    │ <═════════════════════════════════════════> │  Connect
    │                      │                      │
    │                      │  rdv/1 HELLO         │
    │ <─────────────────── < ─────────────────────┤
    │ <═══════════════════════════════════════════╡
    │                      │                      │
    │ rdv/1 CONFIRM        |                      │
    ├───────────────────── ? ───────────────────> │  Confirm
    │                      │                      │
    │ <~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~> │  (Authentication)
    │                      │                      │
    ┴                      ┴                      ┴
```

**Signaling**: Peers agree on an arbitrary one-time token and an rdv server to use, over an
application-specific side channel. The token may be generated by the dialing peer.

**Request**: Each peer opens an `SO_REUSEPORT` socket, which is used through out the attempt.
They dial the rdv server over ipv4 with a `http/1.1 DIAL` or `ACCEPT` request:

-   `Connection: upgrade`
-   `Upgrade: rdv/1`, for upgrading the http conn to TCP for relaying.
-   `Rdv-Token`: The chosen token.
-   `Rdv-Self-Addrs`: A list of self-reported ip:port addresses. By default,
    all local unicast addrs are used, except private ipv6 addresses.
-   Optional application-defined headers (e.g. auth tokens)

**Response**: Once both peers are present, the server responds with a `101 Switching Protocols`:

-   `Connection: upgrade`
-   `Upgrade: rdv/1`
-   `Rdv-Observed-Addr`: The server-observed ipv4:port of the request, for diagnostic purposes.
    This serves the same purpose as [STUN](https://en.wikipedia.org/wiki/STUN).
-   `Rdv-Peer-Addrs`: The other peer's candidate addresses, consisting of both the self-reported and
    the server-observed addresses.
-   Optional application-defined headers

The connection remains open to be used as a relay. This serves the same purpose as
[TURN](https://en.wikipedia.org/wiki/Traversal_Using_Relays_around_NAT).

**Connect**: Clients simultenously listen and dial each other on all candidate peer addrs,
which opens up firewalls and NATs for incoming traffic.
The accepting peer sends an rdv-specific `rdv/1 HELLO <TOKEN>` header on all opened
connections (including the relay), to detect misdials. Note that some connections may result in
[TCP simultenous open](https://ttcplinux.sourceforge.net/documents/one/tcpstate/tcpstate.html).

**Confirm**: The dialing peer chooses a connection by sending `rdv/1 CONFIRM <TOKEN>`. By default,
the first available p2p connection is chosen, or the relay is used after 2 seconds.
All other conns, and the socket, are closed.

**Authentication**: Peers should authenticate each other over an application-defined protocol,
such as TLS or Noise. Authentication is not handled by rdv.
