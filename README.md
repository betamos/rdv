# Rdv: universal tcp connectivity

[![Go Reference](https://pkg.go.dev/badge/github.com/betamos/rdv.svg)](https://pkg.go.dev/github.com/betamos/rdv)

Rdv is a relay-assisted p2p connectivity library that quickly and reliably
establishes a TCP connection between two peers in any network topology,
with a relay fallback if needed. The library provides:

* A client for dialing and accepting connections
* A horizontally scalable http-based server, which acts as a rendezvous point and relay for clients
* A CLI-tool for testing client and server

## Why?

Centralized apps can get lower latency, higher bandwidth and reduced operational costs,
compared to sending p2p data through your servers.
Decentralized apps can increase availability and QoS by having an optional set of rdv servers.
Relays are necessary in some topologies where p2p isn't possible.

You can also think of rdv as a <1000 LoC, minimal config alternative to WebRTC, but for non-realtime
use-cases.

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
since there's no security):

```sh
./rdv dial ... < my_lastpass_vault.zip  # A publicly available file
./rdv accept ... > vault.zip  # Seriously, don't send anything sensitive
```

## How does it work?

Under the hood, rdv repackages a number of highly effective p2p techniques, notably
STUN, TURN and TCP simultaneous open, into a single http request, which doubles as a relay if
needed:

**Request**: Both clients call the server endpoint over ipv4 with their self-reported ip:port
addresses for p2p connectivity.

**Response**: The server responds to both clients with the addresses of their peer, consisting of
both the self-reported- and a server-observed public ipv4:port address.

**Connect**: Both clients listen for and dial each other simultaneously over TCP until the dialer
chooses the connection to use. By default, it picks the first available p2p connection
or falls back to the relay after 2 seconds.

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
ipv4:port of clients, also known as the *observed address*. In some environments, this is harder
than it should be.

To check whether the rdv server gets the right address, go through the quick start guide above
(with the rdv server deployed to your real server environment),
and check the CLI output:

```sh
NOTICE: missing observed address
```

If you see that notice, you need to figure out who is meddling with your traffic, typically
a reverse proxy or a managed cloud provider.
Ask them to kindly
forward the source ip and port to your http server, by adding http headers such as `X-Forwarded-For`
and `X-Forwarded-Port` to inbound http requests.
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
