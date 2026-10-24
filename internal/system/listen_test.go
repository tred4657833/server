package system

import "testing"

const procTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:0613 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 1 1 0
   1: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 2 1 0
   2: 0100007F:0614 0100007F:9999 01 00000000:00000000 00:00000000 00000000  1000        0 3 1 0
`
const procTCP6 = `  sl  local_address                         remote_address                        st
   0: 00000000000000000000000001000000:0615 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  0 0 4 1 0
   1: 00000000000000000000000000000000:0616 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  0 0 5 1 0
`

func TestParseProcNet(t *testing.T) {
	if got := parseProcNet(procTCP, 0x613); len(got) != 1 || !got[0].IsLoopback() || !OnlyLoopback(got) {
		t.Errorf("1555 debía estar solo en loopback: %v", got)
	}
	if got := parseProcNet(procTCP, 0x1F90); len(got) != 1 || !got[0].IsUnspecified() || OnlyLoopback(got) {
		t.Errorf("8080 debía estar en todas las interfaces: %v", got)
	}
	if got := parseProcNet(procTCP, 0x614); len(got) != 0 {
		t.Errorf("una conexión ESTABLISHED no es un LISTEN: %v", got)
	}
	if got := parseProcNet(procTCP6, 0x615); len(got) != 1 || !got[0].IsLoopback() {
		t.Errorf("::1 debía ser loopback: %v", got)
	}
	if got := parseProcNet(procTCP6, 0x616); len(got) != 1 || !got[0].IsUnspecified() {
		t.Errorf(":: debía ser comodín: %v", got)
	}
	if got := parseProcNet(procTCP, 9); len(got) != 0 {
		t.Errorf("nadie escucha en 9: %v", got)
	}
}
