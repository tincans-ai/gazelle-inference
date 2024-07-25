package playht

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	Epoch          = 1519257480
	DefaultAPIURL  = "https://api.play.ht/api"
	DefaultGRPCURL = "prod.turbo.play.ht:443"
)

type Lease struct {
	data     []byte
	created  int
	duration int
	metadata map[string]interface{}
}

func NewLease(data []byte) (*Lease, error) {
	if len(data) < 72 {
		return nil, errors.New("invalid data length")
	}

	lease := &Lease{
		data: data,
	}
	lease.created = int(binary.BigEndian.Uint32(data[64:68]))
	lease.duration = int(binary.BigEndian.Uint32(data[68:72]))
	if err := json.Unmarshal(data[72:], &lease.metadata); err != nil {
		return nil, err
	}

	return lease, nil
}

func (l *Lease) Expires() time.Time {
	return time.Unix(int64(l.created+l.duration+Epoch), 0)
}

func (l *Lease) GRPCAddr() (string, bool) {
	addr, ok := l.metadata["inference_address"].(string)
	return addr, ok
}

func (l *Lease) PremiumGRPCAddr() (string, bool) {
	addr, ok := l.metadata["premium_inference_address"].(string)
	return addr, ok
}

func GetLease(userID, apiKey, apiURL string) (*Lease, error) {
	authHeader := "Bearer " + apiKey
	if bytes.HasPrefix([]byte(apiKey), []byte("Bearer ")) {
		authHeader = apiKey
	}

	client := &http.Client{
		Timeout: time.Second * 10,
	}
	req, err := http.NewRequest("POST", fmt.Sprintf("%s/v2/leases", apiURL), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-User-Id", userID)
	req.Header.Set("Authorization", authHeader)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if (resp.StatusCode != 200) && (resp.StatusCode != 201) {
		return nil, fmt.Errorf("got status code %d", resp.StatusCode)
	}

	data := make([]byte, resp.ContentLength)
	_, err = resp.Body.Read(data)
	if err != nil {
		return nil, err
	}

	lease, err := NewLease(data)
	if err != nil {
		return nil, err
	}

	if lease.Expires().Before(time.Now()) {
		return nil, errors.New("got an expired lease, is your system clock correct?")
	}

	return lease, nil
}

func (l *Lease) Data() []byte {
	return l.data
}

func (l *Lease) GetInferenceAddress() *string {
	addr, ok := l.metadata["inference_address"].(string)
	if !ok {
		return nil
	}
	return &addr
}

func (l *Lease) GetPremiumInferenceAddress() *string {
	addr, ok := l.metadata["premium_inference_address"].(string)
	if !ok {
		return nil
	}
	return &addr
}

type LeaseFactory struct {
	userID string
	apiKey string
	apiURL string
}

func (f *LeaseFactory) NewLease() (*Lease, error) {
	return GetLease(f.userID, f.apiKey, f.apiURL)
}

func NewLeaseFactory(userID, apiKey, apiURL string) *LeaseFactory {
	return &LeaseFactory{
		userID: userID,
		apiKey: apiKey,
		apiURL: apiURL,
	}
}
