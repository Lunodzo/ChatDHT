/*
A demonstration of a simple chat application using libp2p pubsub and DHT.
This program creates a libp2p host, bootstraps a DHT, and creates a pubsub service.
It then allows the user to send messages to other peers in the network.

@Author: Lunodzo Mwinuka
*/

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"sync"

	//"sync"

	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	//peerstore "github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/multiformats/go-multiaddr"
	//"github.com/multiformats/go-multihash"
	"github.com/sirupsen/logrus"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	dutil "github.com/libp2p/go-libp2p/p2p/discovery/util"
)

type Message struct {
	SenderID  string `json:"senderID"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
}

var log = logrus.New()

func main() {
	// Create a context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Generate a key pair for the host
	priv, _, err := crypto.GenerateKeyPair(crypto.RSA, 2048)
	if err != nil {
		log.Fatal(err)
	}

	// Check if this is the first node in the network
	//isFirstPeer := len(os.Args) == 1

	// Dynamic port allocation
	listenAddr, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		log.Fatal("Error creating listening address:", err)
	}

	// Create libp2p host
	host, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrs(listenAddr),
	)
	if err != nil {
		panic(err)
	}

	// Print a complete peer listening address
	for _, addr := range host.Addrs() {
		completePeerAddr := addr.Encapsulate(multiaddr.StringCast("/p2p/" + host.ID().String()))
		fmt.Println("Listening on peer Address:", completePeerAddr)
	}
	

	// Create and bootstrap the kademlia DHT
	kadDHT, err := dht.New(ctx, host) // You can run in client mode (Defaults to server mode)
	if err != nil {
		log.Fatal("Error creating DHT:", err)
	}

	// Bootstrap the DHT
	log.Info("Bootstrapping DHT...")
	if err := kadDHT.Bootstrap(ctx); err != nil {
		log.Error("Error bootstrapping DHT:", err)
	}

	// Wait for the DHT to be fully bootstrapped
	var wg sync.WaitGroup
	for _, peerAddr := range dht.DefaultBootstrapPeers{
		peerInfo, _ := peer.AddrInfoFromP2pAddr(peerAddr)
		wg.Add(1)
		go func(){
			defer wg.Done()
			if err := host.Connect(ctx, *peerInfo); err != nil {
				log.Error("Error connecting to bootstrap node:", err)
			}
		}()
	}
	wg.Wait()
	
	// Handle SIGTERM and interrupt signals
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-c
		log.Warn("Shutting down...")

		// Clean up
		if err := host.Close(); err != nil {
			log.WithError(err).Error("Error shutting down host")
		}
		os.Exit(1)
	}()

	// A go routine to refresh the routing table and periodically find and connect to peers
	go discoverAndConnectPeers(ctx, host, kadDHT, log)

	// Create pubsub
	ps, err := pubsub.NewGossipSub(ctx, host)
	if err != nil {
		panic(err)
	}

	// Handle incoming messages
	host.SetStreamHandler("/topic", handleStream)

	// Start the messaging in a separate goroutine
	go func() {
		var _ *pubsub.PubSub = ps
		startMessaging(host)
	}()

	// Keep the program running
	select {}
}

func discoverAndConnectPeers(ctx context.Context, host host.Host, kadDHT *dht.IpfsDHT, log *logrus.Logger) {
	log.Println("Starting peer discovery...")

	routingDiscovery := drouting.NewRoutingDiscovery(kadDHT)
	dutil.Advertise(ctx, routingDiscovery, "/topic")

	// Look for other peers who have advertised
	anyConnected := false
	for !anyConnected {
		log.Info("Searching for other peers...")
		peerChan, err := routingDiscovery.FindPeers(ctx, "/topic")
		if err != nil {
			log.Error("Error finding peers:", err)
		}

		for peer := range peerChan {
			if peer.ID == host.ID() {
				continue // Skip if the peer is self
			}

			err := host.Connect(ctx, peer)
			if err != nil {
				//log.Error("Error connecting to peer:", err)
			} else {
				anyConnected = true
				log.Info("Connected to peer:", peer)

				/* log.WithFields(logrus.Fields{
				// Close peers
				"numPeersFound": len(kadDHT.RoutingTable().GetPeerInfos()), //TODO: You can uncomment this to see the number of peers found
				}).Info("Found peers in the DHT") */

				// Print the peers in a routing table //TODO: You can uncomment this to see the peers in the routing table
				// for _, peer := range kadDHT.RoutingTable().ListPeers() {
				// 	log.Info("Peer in routing table:", peer)
				// }
			}
		}
	}
}


func handleStream(s network.Stream) {
	fmt.Println("Got a new stream from:", s.Conn().RemotePeer())

	// Create a buffered reader for efficient reading from the stream
	reader := bufio.NewReader(s)

	for {
		// Read message from the stream
		msgBytes, err := reader.ReadBytes('\n') // Assume messages end with newline
		if err != nil {
			log.Error("Error reading from stream:", err)
			return
		}

		// Unmarshal the message (convert from bytes to the Message struct)
		var msg Message
		if err := json.Unmarshal(msgBytes, &msg); err != nil {
			log.Println("Error unmarshaling message:", err)
			continue // Skip to next message if error
		}

		// Process the message (display, store, etc.)
		log.Printf("Received message from %s: %s\n", msg.SenderID, msg.Content)
	}
}

func sendMessage(host host.Host, peerID peer.ID, msg Message) error {
	// Open a new stream to the target peer
	stream, err := host.NewStream(context.Background(), peerID, "/topic")
	if err != nil {
		return err
	}
	defer stream.Close() // Close the stream when done

	// Marshal the message into JSON bytes
	msgBytes, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	// Write the message to the stream
	_, err = stream.Write(msgBytes)
	if err != nil {
		return err
	}

	// Add newline as delimiter
	_, err = stream.Write([]byte("\n"))
	return err
}

func startMessaging(host host.Host) {
	// Get a reader for user input from the console
	reader := bufio.NewReader(os.Stdin)

	for {
		// Prompt the user to enter the recipient ID and message
		fmt.Println("Enter recipient ID (or 'all' for broadcast): ")

		recipientIDStr, _ := reader.ReadString('\n')
		recipientIDStr = strings.TrimSpace(recipientIDStr) // Remove any trailing newline

		fmt.Print("Enter message: ")
		msgContent, _ := reader.ReadString('\n')
		msgContent = strings.TrimSpace(msgContent) // Remove any trailing newline

		// Create the message with the sender's ID (your host's ID)
		msg := Message{
			SenderID:  host.ID().String(),
			Content:   msgContent,
			Timestamp: time.Now().Unix(),
		}

		if recipientIDStr == "all" {
			// Broadcast message to all peers
			for _, peerID := range host.Network().Peers() { // Get all connected peers
				if err := sendMessage(host, peerID, msg); err != nil {
					log.Println("Error sending message to", peerID, ":", err)
				}
			}
		} else {
			// Direct message to specific peer
			peerID, err := peer.Decode(recipientIDStr)
			if err != nil {
				fmt.Println("Invalid peer ID")
				continue
			}
			if err := sendMessage(host, peerID, msg); err != nil {
				fmt.Println("Error sending message:", err)
			}
		}
	}
}
