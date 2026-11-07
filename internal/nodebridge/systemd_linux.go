//go:build libsystemd && (amd64 || arm64)

package nodebridge

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"yagp/pkg/nodegrpc"

	"github.com/ebitengine/purego"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	systemdName    = "org.freedesktop.systemd1"
	systemdPath    = "/org/freedesktop/systemd1"
	systemdManager = systemdName + ".Manager"
	maxGostLogs    = 200
	maxLogBytes    = 4096
)

func gostServiceStatus(ctx context.Context, id uint64) (*nodegrpc.GostServiceStatus, error) {
	api, err := loadSystemd()
	if err != nil {
		return nil, err
	}
	bus, err := api.connect()
	if err != nil {
		return nil, err
	}
	defer api.busUnref(bus)
	info, err := api.serviceStatus(ctx, bus, gostUnit(id))
	return info, systemdRPCError(err)
}

func restartGostService(ctx context.Context, id uint64) (*nodegrpc.GostServiceStatus, error) {
	api, err := loadSystemd()
	if err != nil {
		return nil, err
	}
	bus, err := api.connect()
	if err != nil {
		return nil, err
	}
	defer api.busUnref(bus)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	unit := gostUnit(id)
	job, err := api.managerCall(ctx, bus, "RestartUnit", "ss", unit, "replace")
	if err != nil {
		return nil, systemdRPCError(err)
	}
	if err := api.waitForJob(ctx, bus, job); err != nil {
		return nil, systemdRPCError(err)
	}
	info, err := api.serviceStatus(ctx, bus, unit)
	if err != nil {
		return nil, systemdRPCError(err)
	}
	if info.ActiveState != "active" {
		return nil, status.Errorf(codes.FailedPrecondition, "%s is %s (%s) after restart", unit, info.ActiveState, info.SubState)
	}
	return info, nil
}

func gostServiceLogs(ctx context.Context, id uint64, lines uint32) (*nodegrpc.GostLogsReply, error) {
	api, err := loadSystemd()
	if err != nil {
		return nil, err
	}
	if lines == 0 {
		lines = 100
	}
	// libsystemd journal handles stay on the same OS thread for their lifetime.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	journal, err := api.openJournal(gostUnit(id))
	if err != nil {
		return nil, systemdRPCError(err)
	}
	defer api.journalClose(journal)
	entries, err := api.readJournal(ctx, journal, min(lines, maxGostLogs))
	if err != nil {
		return nil, systemdRPCError(err)
	}
	return &nodegrpc.GostLogsReply{Entries: entries}, nil
}

func gostUnit(id uint64) string {
	return fmt.Sprintf("yagp-gost-%d.service", id)
}

type systemdAPI struct {
	busNew           func(*uintptr) int32
	busSetAddress    func(uintptr, string) int32
	busStart         func(uintptr) int32
	busUnref         func(uintptr) uintptr
	messageNew       func(uintptr, *uintptr, string, string, string, string) int32
	messageAppend    func(uintptr, string, ...any) int32
	messageRead      func(uintptr, string, **byte) int32
	messageEnter     func(uintptr, byte, string) int32
	messageUnref     func(uintptr) uintptr
	busCall          func(uintptr, uintptr, uint64, *nativeBusError, *uintptr) int32
	busErrorFree     func(*nativeBusError)
	journalOpen      func(*uintptr, int32) int32
	journalClose     func(uintptr)
	journalThreshold func(uintptr, uintptr) int32
	journalMatch     func(uintptr, string, uintptr) int32
	journalOr        func(uintptr) int32
	journalTail      func(uintptr) int32
	journalPrevious  func(uintptr) int32
	journalData      func(uintptr, string, **byte, *uintptr) int32
	journalTimestamp func(uintptr, *uint64) int32
}

var loadSystemd = sync.OnceValues(func() (*systemdAPI, error) {
	lib, err := purego.Dlopen("libsystemd.so.0", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, status.Errorf(codes.Unimplemented, "load libsystemd: %v", err)
	}
	api := &systemdAPI{}
	for _, binding := range []struct {
		name   string
		target any
	}{
		{"sd_bus_new", &api.busNew},
		{"sd_bus_set_address", &api.busSetAddress},
		{"sd_bus_start", &api.busStart},
		{"sd_bus_unref", &api.busUnref},
		{"sd_bus_message_new_method_call", &api.messageNew},
		{"sd_bus_message_append", &api.messageAppend},
		{"sd_bus_message_read", &api.messageRead},
		{"sd_bus_message_enter_container", &api.messageEnter},
		{"sd_bus_message_unref", &api.messageUnref},
		{"sd_bus_call", &api.busCall},
		{"sd_bus_error_free", &api.busErrorFree},
		{"sd_journal_open", &api.journalOpen},
		{"sd_journal_close", &api.journalClose},
		{"sd_journal_set_data_threshold", &api.journalThreshold},
		{"sd_journal_add_match", &api.journalMatch},
		{"sd_journal_add_disjunction", &api.journalOr},
		{"sd_journal_seek_tail", &api.journalTail},
		{"sd_journal_previous", &api.journalPrevious},
		{"sd_journal_get_data", &api.journalData},
		{"sd_journal_get_realtime_usec", &api.journalTimestamp},
	} {
		address, err := purego.Dlsym(lib, binding.name)
		if err != nil {
			_ = purego.Dlclose(lib)
			return nil, status.Errorf(codes.Unimplemented, "load libsystemd symbol %s: %v", binding.name, err)
		}
		purego.RegisterFunc(binding.target, address)
	}
	return api, nil
})

func (api *systemdAPI) connect() (uintptr, error) {
	var bus uintptr
	if result := api.busNew(&bus); result < 0 {
		return 0, status.Errorf(codes.Unavailable, "create systemd connection: %v", errno(result))
	}
	// The root-managed bridge can use PID 1 directly, including on hosts without a system D-Bus daemon.
	result := api.busSetAddress(bus, "unix:path=/run/systemd/private")
	if result >= 0 {
		result = api.busStart(bus)
	}
	if result < 0 {
		api.busUnref(bus)
		return 0, status.Errorf(codes.Unavailable, "connect to systemd: %v", errno(result))
	}
	return bus, nil
}

func (api *systemdAPI) serviceStatus(ctx context.Context, bus uintptr, unit string) (*nodegrpc.GostServiceStatus, error) {
	path, err := api.managerCall(ctx, bus, "LoadUnit", "s", unit)
	if hasBusError(err, systemdName+".NoSuchUnit") {
		return &nodegrpc.GostServiceStatus{Unit: unit, LoadState: "not-found", ActiveState: "inactive", SubState: "dead"}, nil
	}
	if err != nil {
		return nil, err
	}
	info := &nodegrpc.GostServiceStatus{Unit: unit}
	for _, property := range []struct {
		name  string
		value *string
	}{
		{"LoadState", &info.LoadState},
		{"ActiveState", &info.ActiveState},
		{"SubState", &info.SubState},
	} {
		*property.value, err = api.callString(ctx, bus, path, "org.freedesktop.DBus.Properties", "Get", "s", true, "ss", systemdName+".Unit", property.name)
		if err != nil {
			return nil, err
		}
	}
	return info, nil
}

func (api *systemdAPI) waitForJob(ctx context.Context, bus uintptr, path string) error {
	idText, ok := strings.CutPrefix(path, systemdPath+"/job/")
	id, err := strconv.ParseUint(idText, 10, 32)
	if !ok || err != nil {
		return fmt.Errorf("unexpected systemd job path %q", path)
	}
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := api.managerCall(ctx, bus, "GetJob", "u", uint32(id))
		if hasBusError(err, systemdName+".NoSuchJob") {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (api *systemdAPI) managerCall(ctx context.Context, bus uintptr, member, signature string, args ...any) (string, error) {
	return api.callString(ctx, bus, systemdPath, systemdManager, member, "o", false, signature, args...)
}

func (api *systemdAPI) callString(ctx context.Context, bus uintptr, path, iface, member, replyType string, variant bool, signature string, args ...any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var request, reply uintptr
	if result := api.messageNew(bus, &request, systemdName, path, iface, member); result < 0 {
		return "", fmt.Errorf("create %s message: %w", member, errno(result))
	}
	defer api.messageUnref(request)
	if result := api.messageAppend(request, signature, args...); result < 0 {
		return "", fmt.Errorf("set %s arguments: %w", member, errno(result))
	}
	timeout := 2 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
	}
	if timeout <= 0 {
		return "", context.DeadlineExceeded
	}
	var replyError nativeBusError
	defer api.busErrorFree(&replyError)
	if result := api.busCall(bus, request, uint64(max(1, timeout.Microseconds())), &replyError, &reply); result < 0 {
		return "", busError{
			method: member, name: nativeString(replyError.name), message: nativeString(replyError.message), err: errno(result),
		}
	}
	defer api.messageUnref(reply)
	if variant && api.messageEnter(reply, 'v', replyType) <= 0 {
		return "", fmt.Errorf("invalid systemd %s variant", member)
	}
	var value *byte
	if api.messageRead(reply, replyType, &value) <= 0 {
		return "", fmt.Errorf("invalid systemd %s reply", member)
	}
	return nativeString(value), nil
}

// nativeBusError mirrors the public sd_bus_error structure in sd-bus.h.
type nativeBusError struct {
	name, message *byte
	needFree      int32
}

type busError struct {
	method, name, message string
	err                   error
}

func (e busError) Error() string {
	if e.message != "" {
		return fmt.Sprintf("systemd %s: %s", e.method, e.message)
	}
	return fmt.Sprintf("systemd %s: %v", e.method, e.err)
}

func (e busError) Unwrap() error { return e.err }

func hasBusError(err error, name string) bool {
	var busErr busError
	return errors.As(err, &busErr) && busErr.name == name
}

func errno(result int32) syscall.Errno { return syscall.Errno(-result) }

func nativeString(value *byte) string {
	if value == nil {
		return ""
	}
	const limit = 512
	length := 0
	for length < limit && *(*byte)(unsafe.Add(unsafe.Pointer(value), length)) != 0 {
		length++
	}
	return string(unsafe.Slice(value, length))
}

func systemdRPCError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, err.Error())
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, syscall.ETIMEDOUT):
		return status.Error(codes.DeadlineExceeded, err.Error())
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		return status.Error(codes.PermissionDenied, err.Error())
	case hasBusError(err, systemdName+".NoSuchUnit"):
		return status.Error(codes.NotFound, err.Error())
	default:
		return status.Error(codes.FailedPrecondition, err.Error())
	}
}

func (api *systemdAPI) openJournal(unit string) (uintptr, error) {
	const localJournal = 1 // SD_JOURNAL_LOCAL_ONLY
	var journal uintptr
	if result := api.journalOpen(&journal, localJournal); result < 0 {
		return 0, fmt.Errorf("open systemd journal: %w", errno(result))
	}
	if err := api.filterJournal(journal, unit); err != nil {
		api.journalClose(journal)
		return 0, err
	}
	return journal, nil
}

func (api *systemdAPI) filterJournal(journal uintptr, unit string) error {
	if result := api.journalThreshold(journal, uintptr(maxLogBytes+len("MESSAGE="))); result < 0 {
		return fmt.Errorf("limit journal entry size: %w", errno(result))
	}
	if result := api.journalMatch(journal, "_SYSTEMD_UNIT="+unit, 0); result < 0 {
		return fmt.Errorf("filter unit logs: %w", errno(result))
	}
	// Include PID 1 messages about the unit, such as failed startup and restart.
	if result := api.journalOr(journal); result < 0 {
		return fmt.Errorf("filter manager logs: %w", errno(result))
	}
	for _, match := range []string{"_PID=1", "UNIT=" + unit} {
		if result := api.journalMatch(journal, match, 0); result < 0 {
			return fmt.Errorf("filter manager logs: %w", errno(result))
		}
	}
	if result := api.journalTail(journal); result < 0 {
		return fmt.Errorf("seek end of journal: %w", errno(result))
	}
	return nil
}

func (api *systemdAPI) readJournal(ctx context.Context, journal uintptr, lines uint32) ([]*nodegrpc.GostLogEntry, error) {
	entries := make([]*nodegrpc.GostLogEntry, 0, lines)
	for range lines {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result := api.journalPrevious(journal)
		if result == 0 {
			break
		}
		if result < 0 {
			return nil, fmt.Errorf("read journal entry: %w", errno(result))
		}
		var timestamp uint64
		if result := api.journalTimestamp(journal, &timestamp); result < 0 {
			return nil, fmt.Errorf("read journal timestamp: %w", errno(result))
		}
		message, err := api.journalField(journal, "MESSAGE", maxLogBytes)
		if err != nil {
			return nil, err
		}
		priority, err := api.journalField(journal, "PRIORITY", 1)
		if err != nil {
			return nil, err
		}
		level := int32(6)
		if len(priority) == 1 && priority[0] >= '0' && priority[0] <= '7' {
			level = int32(priority[0] - '0')
		}
		entries = append(entries, &nodegrpc.GostLogEntry{
			TimestampUnixMicros: int64(timestamp), Message: strings.ToValidUTF8(message, "�"), Priority: level,
		})
	}
	slices.Reverse(entries)
	return entries, nil
}

func (api *systemdAPI) journalField(journal uintptr, field string, limit uintptr) (string, error) {
	var data *byte
	var size uintptr
	result := api.journalData(journal, field, &data, &size)
	if result < 0 {
		if errno(result) == syscall.ENOENT || errno(result) == syscall.E2BIG {
			return "", nil
		}
		return "", fmt.Errorf("read journal %s: %w", field, errno(result))
	}
	prefix := uintptr(len(field) + 1)
	if size <= prefix {
		return "", nil
	}
	return string(unsafe.Slice((*byte)(unsafe.Add(unsafe.Pointer(data), prefix)), min(size-prefix, limit))), nil
}
