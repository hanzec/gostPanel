package nodebridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"yagp/pkg/nodegrpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const trafficQuotaName = "traffic"
const singleNodeQuotaTable = "yagp_quota"

type trafficQuotaState struct {
	LimitBytes  uint64   `json:"limit_bytes"`
	UsedBytes   uint64   `json:"used_bytes"`
	WindowStart int64    `json:"window_start"`
	TCPPorts    []uint32 `json:"tcp_ports"`
	UDPPorts    []uint32 `json:"udp_ports"`
	calibrating bool
}

func (s *controlServer) SyncTrafficQuota(ctx context.Context, request *nodegrpc.TrafficQuotaRequest) (*nodegrpc.TrafficQuotaReply, error) {
	next := trafficQuotaState{
		LimitBytes: request.GetLimitBytes(), UsedBytes: request.GetUsedBytes(), WindowStart: request.GetWindowStart(),
		TCPPorts: request.GetTcpPorts(), UDPPorts: request.GetUdpPorts(),
	}
	if request.CalibratedUsedBytes != nil {
		next.UsedBytes, next.calibrating = request.GetCalibratedUsedBytes(), true
	}
	used, err := syncTrafficQuota(ctx, s.config, next)
	if err != nil {
		return nil, err
	}
	return &nodegrpc.TrafficQuotaReply{UsedBytes: used, Calibrated: next.calibrating}, nil
}

func restoreTrafficQuota(ctx context.Context, cfg Config) error {
	previous, err := readTrafficQuotaState(trafficQuotaPath(cfg.ConfigFile))
	if err != nil || previous == nil || previous.LimitBytes == 0 {
		return err
	}
	_, err = syncTrafficQuota(ctx, cfg, *previous)
	return err
}

func syncTrafficQuota(ctx context.Context, cfg Config, next trafficQuotaState) (uint64, error) {
	if runtime.GOOS != "linux" || cfg.ConfigFile == "" {
		return 0, status.Error(codes.Unimplemented, "traffic quota requires a Linux node with a configuration file")
	}
	clearing := next.LimitBytes == 0 && next.WindowStart == -1
	if next.LimitBytes > math.MaxInt64 || (next.WindowStart < 0 && !clearing) ||
		(next.calibrating && (next.LimitBytes == 0 || next.UsedBytes > math.MaxInt64)) ||
		!validTrafficQuotaPorts(next.TCPPorts) || !validTrafficQuotaPorts(next.UDPPorts) {
		return 0, status.Error(codes.InvalidArgument, "invalid traffic quota limit, window or ports")
	}
	next.TCPPorts = normalizeTrafficQuotaPorts(next.TCPPorts)
	next.UDPPorts = normalizeTrafficQuotaPorts(next.UDPPorts)
	nft, err := exec.LookPath("nft")
	if err != nil {
		return 0, status.Error(codes.Unimplemented, "nftables is unavailable: install the nftables package on this node")
	}
	path := trafficQuotaPath(cfg.ConfigFile)
	table := fmt.Sprintf("yagp_quota_%d", cfg.NodeID)
	var legacy string
	if cfg.RuntimeLayout >= singleNodeLayoutVersion {
		table, legacy = singleNodeQuotaTable, table
	}
	nftApplyMu.Lock()
	defer nftApplyMu.Unlock()
	previous, err := readTrafficQuotaState(path)
	if err != nil {
		return 0, status.Errorf(codes.Internal, "read traffic quota state: %v", err)
	}
	live, err := readLiveTrafficQuota(ctx, nft, table)
	if err != nil {
		return 0, trafficQuotaNftError(ctx, "read traffic quota", err)
	}
	if live == nil && legacy != "" {
		live, err = readLiveTrafficQuota(ctx, nft, legacy)
		if err != nil {
			return 0, trafficQuotaNftError(ctx, "read legacy traffic quota", err)
		}
	}
	if clearing {
		next.UsedBytes = 0
		next.TCPPorts, next.UDPPorts = nil, nil
	} else if !next.calibrating && (previous == nil || previous.WindowStart == next.WindowStart) {
		if previous != nil {
			next.UsedBytes = max(next.UsedBytes, previous.UsedBytes)
		}
		if live != nil {
			next.UsedBytes = max(next.UsedBytes, live.Used)
		}
	}
	if previous == nil && live == nil && next.LimitBytes == 0 {
		return next.UsedBytes, nil
	}
	samePolicy := previous != nil && previous.samePolicy(next)
	replace := live != nil || (previous != nil && previous.LimitBytes != 0)
	if next.LimitBytes != 0 {
		replace = next.calibrating || !samePolicy || live == nil || live.Table != table || live.Bytes != next.LimitBytes || live.Used < min(next.UsedBytes, next.LimitBytes)
	}
	if !replace && samePolicy && previous.UsedBytes == next.UsedBytes {
		return next.UsedBytes, nil
	}
	var batch []byte
	if replace {
		if live != nil && live.Table != table {
			batch = trafficQuotaBatch(live.Table, trafficQuotaState{})
		}
		batch = append(batch, trafficQuotaBatch(table, next)...)
	}
	if err := applyTrafficQuota(ctx, nft, path, next, batch); err != nil {
		return 0, err
	}
	return next.UsedBytes, nil
}

func (s trafficQuotaState) samePolicy(other trafficQuotaState) bool {
	return s.LimitBytes == other.LimitBytes && s.WindowStart == other.WindowStart &&
		slices.Equal(s.TCPPorts, other.TCPPorts) && slices.Equal(s.UDPPorts, other.UDPPorts)
}

func applyTrafficQuota(ctx context.Context, nft, path string, next trafficQuotaState, batch []byte) error {
	encoded, err := json.Marshal(next)
	if err != nil {
		return status.Errorf(codes.Internal, "encode traffic quota state: %v", err)
	}
	staged, err := stageNftConfig(path, string(encoded))
	if err != nil {
		return status.Errorf(codes.Internal, "stage traffic quota state: %v", err)
	}
	defer os.Remove(staged)
	if len(batch) != 0 {
		if err := runNft(ctx, nft, batch, "-f", "-"); err != nil {
			return trafficQuotaNftError(ctx, "apply traffic quota", err)
		}
	}
	if err := os.Rename(staged, path); err != nil {
		return status.Errorf(codes.Internal, "could not save traffic quota state: %v", err)
	}
	return nil
}

func trafficQuotaBatch(table string, next trafficQuotaState) []byte {
	var batch strings.Builder
	// Add then delete is idempotent, including on kernels without the destroy command.
	fmt.Fprintf(&batch, "add table inet %s\ndelete table inet %s\n", table, table)
	if next.LimitBytes == 0 {
		return []byte(batch.String())
	}
	fmt.Fprintf(&batch, "add table inet %s\n", table)
	// The kernel rejects an initial count above the limit. Keep the full count in the state file.
	fmt.Fprintf(&batch, "add quota inet %s %s { over %d bytes used %d bytes; }\n",
		table, trafficQuotaName, next.LimitBytes, min(next.UsedBytes, next.LimitBytes))
	fmt.Fprintf(&batch, "add chain inet %s ingress { type filter hook input priority -10; policy accept; }\n", table)
	fmt.Fprintf(&batch, "add chain inet %s egress { type filter hook output priority -10; policy accept; }\n", table)
	appendTrafficQuotaRules(&batch, table, "tcp", next.TCPPorts)
	appendTrafficQuotaRules(&batch, table, "udp", next.UDPPorts)
	return []byte(batch.String())
}

func appendTrafficQuotaRules(batch *strings.Builder, table, protocol string, ports []uint32) {
	if len(ports) == 0 {
		return
	}
	numbers := make([]string, len(ports))
	for i, port := range ports {
		numbers[i] = strconv.FormatUint(uint64(port), 10)
	}
	set := strings.Join(numbers, ", ")
	fmt.Fprintf(batch, "add rule inet %s ingress %s dport { %s } quota name %q drop\n", table, protocol, set, trafficQuotaName)
	fmt.Fprintf(batch, "add rule inet %s egress %s sport { %s } quota name %q drop\n", table, protocol, set, trafficQuotaName)
}

func readTrafficQuotaState(path string) (*trafficQuotaState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state trafficQuotaState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

type liveTrafficQuota struct {
	Table string `json:"table"`
	Name  string `json:"name"`
	Bytes uint64 `json:"bytes"`
	Used  uint64 `json:"used"`
}

func readLiveTrafficQuota(ctx context.Context, nft, table string) (*liveTrafficQuota, error) {
	out, err := nftOutput(ctx, nft, nil, "-j", "list", "quotas", "inet")
	if err != nil {
		return nil, err
	}
	var result struct {
		Objects []struct {
			Quota *liveTrafficQuota `json:"quota"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, err
	}
	for _, object := range result.Objects {
		if q := object.Quota; q != nil && q.Table == table && q.Name == trafficQuotaName {
			return q, nil
		}
	}
	return nil, nil
}

func trafficQuotaPath(configFile string) string {
	return filepath.Join(filepath.Dir(configFile), ".traffic-quota.json")
}

func validTrafficQuotaPorts(ports []uint32) bool {
	for _, port := range ports {
		if port == 0 || port > 65535 {
			return false
		}
	}
	return true
}

func normalizeTrafficQuotaPorts(ports []uint32) []uint32 {
	ports = slices.Clone(ports)
	slices.Sort(ports)
	return slices.Compact(ports)
}

func trafficQuotaNftError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return status.FromContextError(ctx.Err()).Err()
	}
	return status.Errorf(codes.FailedPrecondition, "%s: %v", operation, err)
}
