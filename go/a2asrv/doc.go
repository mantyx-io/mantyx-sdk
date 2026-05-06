// Package a2asrv exposes a MANTYX agent as an Agent2Agent (A2A) peer.
//
// The package wraps the official A2A Go SDK
// (github.com/a2aproject/a2a-go/v2) so that other agents can discover and
// call a MANTYX agent over JSON-RPC, REST, or gRPC. It implements
// a2asrv.AgentExecutor on top of *mantyx.Client and ships a small Serve
// helper that mounts the executor under net/http for prototyping.
//
// Importing this package pulls in the official A2A Go SDK; consumers who
// don't need to expose an agent should not import a2asrv and won't pay any
// transitive cost.
//
// Quickstart:
//
//	import (
//	    mantyx "github.com/mantyx-io/mantyx-go-sdk"
//	    "github.com/mantyx-io/mantyx-go-sdk/a2asrv"
//	    "github.com/a2aproject/a2a-go/v2/a2a"
//	)
//
//	client := mantyx.NewClient(mantyx.Options{
//	    APIKey:        os.Getenv("MANTYX_API_KEY"),
//	    WorkspaceSlug: os.Getenv("MANTYX_WORKSPACE_SLUG"),
//	})
//
//	handle, err := a2asrv.Serve(ctx, a2asrv.ServeOptions{
//	    Client: client,
//	    Agent:  a2asrv.AgentSpec{AgentID: "agent_cm6abc123"},
//	    AgentCard: &a2a.AgentCard{
//	        Name:        "Acme Support",
//	        Description: "Customer support agent",
//	        SupportedInterfaces: []*a2a.AgentInterface{
//	            a2a.NewAgentInterface("http://localhost:4000/", a2a.TransportProtocolJSONRPC),
//	        },
//	        Capabilities:       a2a.AgentCapabilities{Streaming: true},
//	        DefaultInputModes:  []string{"text"},
//	        DefaultOutputModes: []string{"text"},
//	        Skills: []a2a.AgentSkill{{
//	            ID: "chat", Name: "Chat", Tags: []string{"chat"},
//	        }},
//	    },
//	    Addr: ":4000",
//	})
//	if err != nil { log.Fatal(err) }
//	defer handle.Close(context.Background())
//	<-ctx.Done()
package a2asrv
