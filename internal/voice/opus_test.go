package voice

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func loopbackPeer(t *testing.T) *webrtc.PeerConnection {
	t.Helper()
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	settings.SetIPFilter(func(ip net.IP) bool { return ip.Equal(net.IPv4(127, 0, 0, 1)) })
	settings.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	api, err := NewOpusAPI(settings)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	return peer
}

func gather(t *testing.T, ctx context.Context, peer *webrtc.PeerConnection, desc webrtc.SessionDescription) {
	t.Helper()
	done := webrtc.GatheringCompletePromise(peer)
	if err := peer.SetLocalDescription(desc); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func connectPeers(t *testing.T, ctx context.Context, source, destination *webrtc.PeerConnection) {
	t.Helper()
	offer, err := source.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(offer.SDP, "m=video") || strings.Contains(offer.SDP, "m=application") {
		t.Fatal("РЅРµРѕР¶РёРґР°РЅРЅС‹Р№ С‚РёРї РјРµРґРёР°")
	}
	gather(t, ctx, source, offer)
	if err := destination.SetRemoteDescription(*source.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := destination.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather(t, ctx, destination, answer)
	if err := source.SetRemoteDescription(*destination.LocalDescription()); err != nil {
		t.Fatal(err)
	}
}

func addOpus(t *testing.T, peer *webrtc.PeerConnection, id string) *webrtc.TrackLocalStaticRTP {
	t.Helper()
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, id, "voice")
	if err != nil {
		t.Fatal(err)
	}
	sender, err := peer.AddTrack(track)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buffer := make([]byte, 1500)
		for {
			if _, _, err := sender.Read(buffer); err != nil {
				return
			}
		}
	}()
	return track
}

// РџСЂРѕРІРµСЂСЏРµС‚ РЅР°СЃС‚РѕСЏС‰РёРµ ICE/DTLS/SRTP РЅР° loopback Рё РїРµСЂРµСЃС‹Р»РєСѓ Opus RTP С‡РµСЂРµР·
// РѕС‚РґРµР»СЊРЅС‹Рµ РІС…РѕРґСЏС‰РµРµ/РёСЃС…РѕРґСЏС‰РµРµ СЃРѕРµРґРёРЅРµРЅРёСЏ СЃРµСЂРІРµСЂР°. РњРёРєСЂРѕС„РѕРЅ РЅРµ РёСЃРїРѕР»СЊР·СѓРµС‚СЃСЏ.
func TestOpusRelayLoopback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	publisher, input := loopbackPeer(t), loopbackPeer(t)
	output, listener := loopbackPeer(t), loopbackPeer(t)
	source := addOpus(t, publisher, "source")
	forward := addOpus(t, output, "forward")
	received := make(chan []byte, 1)
	input.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			if forward.WriteRTP(packet) != nil {
				return
			}
		}
	})
	listener.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		packet, _, err := track.ReadRTP()
		if err == nil {
			select {
			case received <- packet.Payload:
			default:
			}
		}
	})
	connectPeers(t, ctx, output, listener)
	connectPeers(t, ctx, publisher, input)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	payload := []byte{0xf8, 0xff, 0xfe}
	var sequence uint16
	for {
		select {
		case got := <-received:
			if !bytes.Equal(got, payload) {
				t.Fatalf("Р±Р°Р№С‚С‹ РёР·РјРµРЅРµРЅС‹: %x", got)
			}
			return
		case <-ticker.C:
			sequence++
			packet := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: sequence, Timestamp: uint32(sequence) * 960, SSRC: 42}, Payload: payload}
			if err := source.WriteRTP(packet); err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("Р·РІСѓРє РЅРµ РїСЂРѕС€С‘Р» С‡РµСЂРµР· СЂРµС‚СЂР°РЅСЃР»СЏС‚РѕСЂ", ctx.Err())
		}
	}
}
