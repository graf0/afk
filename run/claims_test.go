package run

import (
	"bufio"
	"os"
	"os/exec"
	"testing"
	"time"
)

// claimHolderEnv makes the test binary hold claims instead of testing:
// the directory, then the ticket numbers.
const claimHolderEnv = "AFK_TEST_CLAIM_DIR"

func TestClaims(t *testing.T) {
	c := Claims{Dir: t.TempDir() + "/claims/o-r"}
	release, ok, err := c.Claim(1, 2)
	if err != nil || !ok {
		t.Fatalf("Claim(1, 2) = %v, %v", ok, err)
	}
	// Another Factory, even in this process: all or nothing.
	if _, ok, err = c.Claim(3, 2); err != nil || ok {
		t.Errorf("Claim(3, 2) = %v, %v; want refused: #2 is held", ok, err)
	}
	third, ok, err := c.Claim(3)
	if err != nil || !ok {
		t.Errorf("Claim(3) = %v, %v; want free: the refused Claim(3, 2) gave #3 back", ok, err)
	}
	third()
	release()
	again, ok, err := c.Claim(2, 1)
	if err != nil || !ok {
		t.Errorf("Claim(2, 1) after release = %v, %v", ok, err)
	}
	again()
}

// Two processes of afk don't take the same ticket, and a killed one gives
// its claims back.
func TestClaimsAcrossProcesses(t *testing.T) {
	c := Claims{Dir: t.TempDir()}
	holder := exec.Command(os.Args[0], "-test.run=^TestClaimHolder$")
	holder.Env = append(os.Environ(), claimHolderEnv+"="+c.Dir)
	stdout, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill(); _ = holder.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "claimed\n" {
		t.Fatalf("the holder said %q, %v", line, err)
	}

	if _, ok, err := c.Claim(7); err != nil || ok {
		t.Errorf("Claim(7) = %v, %v; want refused: another process holds #7", ok, err)
	}
	free, ok, err := c.Claim(8)
	if err != nil || !ok {
		t.Errorf("Claim(8) = %v, %v; want free", ok, err)
	}
	free()

	if err = holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	release, ok, err := c.Claim(7)
	if err != nil || !ok {
		t.Fatalf("Claim(7) after the holder died = %v, %v; want free", ok, err)
	}
	release()
}

// TestClaimHolder is the other process of TestClaimsAcrossProcesses: it
// claims #7, says so and waits to be killed.
func TestClaimHolder(t *testing.T) {
	dir := os.Getenv(claimHolderEnv)
	if dir == "" {
		t.Skip("helper of TestClaimsAcrossProcesses")
	}
	if _, ok, err := (Claims{Dir: dir}).Claim(7); err != nil || !ok {
		t.Fatalf("Claim(7) = %v, %v", ok, err)
	}
	_, _ = os.Stdout.WriteString("claimed\n")
	time.Sleep(time.Minute)
}
