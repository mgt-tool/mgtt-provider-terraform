package probes

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mgt-tool/mgtt/sdk/provider"
)

// fakeTF builds a TF whose Exec returns scripted responses keyed on the
// first meaningful arg (subcommand like "state"/"plan"/"show"/"workspace").
// Used to lock probe behavior without invoking real terraform.
func fakeTF(responses map[string]fakeResponse) *TF {
	return &TF{
		Binary:  "terraform",
		Workdir: "/does/not/matter",
		Exec: func(ctx context.Context, dir, workspace string, args ...string) ([]byte, []byte, int, error) {
			key := routeKey(args)
			if r, ok := responses[key]; ok {
				return []byte(r.stdout), []byte(r.stderr), r.code, r.err
			}
			return nil, []byte("unroutable in fake: " + strings.Join(args, " ")),
				0, errors.New("unroutable")
		},
	}
}

type fakeResponse struct {
	stdout string
	stderr string
	code   int
	err    error
}

// routeKey joins the first two argv tokens so "state list" and "show -json"
// are distinct keys. It skips flags (-foo).
func routeKey(args []string) string {
	parts := []string{}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		parts = append(parts, a)
		if len(parts) == 2 {
			break
		}
	}
	return strings.Join(parts, " ")
}

func probe(t *testing.T, register func(*provider.Registry), tf *TF, req provider.Request) provider.Result {
	t.Helper()
	r := provider.NewRegistry()
	register(r)
	// Inject the fake TF via a trampoline: swap newFromReq's result by
	// supplying the workdir/bin from the fake and a workspace select that
	// doesn't hit the fake. We cannot monkey-patch newFromReq, so the
	// tests rely on the fake's router + req.Extra["workdir"] being set.
	_ = tf // the fake is supplied via the probe flow below
	result, err := r.Probe(context.Background(), req)
	if err != nil {
		t.Fatalf("probe %+v: %v", req, err)
	}
	return result
}

// The probe code calls New() internally via newFromReq, which bypasses our
// fake. For this first test pass we exercise the pure helpers and the
// registry wiring; integration tests against real terraform live in a
// separate build tag (test/integration/).

func TestTF_Run_ClassifiesLockStderr(t *testing.T) {
	tf := fakeTF(map[string]fakeResponse{
		"plan": {
			stderr: "Error: Error acquiring the state lock",
			code:   1,
			err:    errors.New("exit 1"),
		},
	})
	_, _, err := tf.Run(context.Background(), "plan", "-detailed-exitcode")
	if !errors.Is(err, provider.ErrTransient) {
		t.Fatalf("want ErrTransient from state-lock stderr, got %v", err)
	}
}

func TestTF_Run_DetailedExitCodePassesThrough(t *testing.T) {
	tf := fakeTF(map[string]fakeResponse{
		"plan": {stdout: "", stderr: "", code: 2, err: nil},
	})
	_, code, err := tf.Run(context.Background(), "plan", "-detailed-exitcode", "-target=foo")
	if err != nil {
		t.Fatal(err)
	}
	if code != 2 {
		t.Fatalf("want code 2 (changes), got %d", code)
	}
}

func TestTF_StateList_ParsesLines(t *testing.T) {
	tf := fakeTF(map[string]fakeResponse{
		"state list": {
			stdout: "aws_db_instance.main\naws_elasticache_cluster.sessions\n\n",
		},
	})
	addrs, err := tf.StateList(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) != 2 {
		t.Fatalf("want 2 addresses, got %v", addrs)
	}
	if addrs[0] != "aws_db_instance.main" {
		t.Fatalf("order preserved? got %v", addrs)
	}
}

func TestCountDependents_DependsOnAndExpressionRefs(t *testing.T) {
	// Two dependents via depends_on (values.root_module) and one more
	// via configuration.expressions.*.references. A substring-prefix
	// sibling (main_replica) must NOT count.
	blob := []byte(`{
		"values": {
			"root_module": {
				"resources": [
					{"address":"aws_db_instance.main"},
					{"address":"aws_db_instance.main_replica"},
					{"address":"aws_ecs_service.api","depends_on":["aws_db_instance.main"]},
					{"address":"aws_route53_record.api","depends_on":["aws_db_instance.main"]}
				]
			}
		},
		"configuration": {
			"root_module": {
				"resources": {
					"aws_ssm_parameter.rds_endpoint": {
						"expressions": {
							"value": {
								"references": ["aws_db_instance.main.endpoint", "aws_db_instance.main"]
							}
						}
					}
				}
			}
		}
	}`)
	n, err := countDependents(blob, "aws_db_instance.main")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("want 3 dependents, got %d", n)
	}
}

func TestCountDependents_IgnoresDescriptionStrings(t *testing.T) {
	// A tag/description containing the address as a user-authored string
	// must not count — the main failure mode of the substring implementation.
	blob := []byte(`{
		"values": {
			"root_module": {
				"resources": [
					{"address":"aws_db_instance.main"},
					{"address":"aws_s3_bucket.backup"}
				]
			}
		},
		"configuration": {
			"root_module": {
				"resources": {
					"aws_s3_bucket.backup": {
						"expressions": {
							"tags": {
								"constant_value": {"purpose": "backup for aws_db_instance.main"}
							}
						}
					}
				}
			}
		}
	}`)
	n, err := countDependents(blob, "aws_db_instance.main")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("description string must not count; got %d", n)
	}
}

func TestCountDependents_ChildModuleRecursion(t *testing.T) {
	blob := []byte(`{
		"values": {
			"root_module": {
				"resources": [{"address":"aws_db_instance.main"}],
				"child_modules": [
					{
						"resources": [
							{"address":"module.app.aws_ecs_service.api","depends_on":["aws_db_instance.main"]}
						]
					}
				]
			}
		},
		"configuration": {"root_module": {}}
	}`)
	n, err := countDependents(blob, "aws_db_instance.main")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("child module dependent must count; got %d", n)
	}
}

func TestCountDependents_NoReferences(t *testing.T) {
	blob := []byte(`{"values":{"root_module":{}},"configuration":{"root_module":{}}}`)
	n, err := countDependents(blob, "aws_db_instance.main")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("want 0, got %d", n)
	}
}

func TestResolveWorkdir_RequiresFlag(t *testing.T) {
	_, err := ResolveWorkdir(provider.Request{})
	if !errors.Is(err, provider.ErrUsage) {
		t.Fatalf("empty workdir should be ErrUsage, got %v", err)
	}
	got, err := ResolveWorkdir(provider.Request{Extra: map[string]string{"workdir": "/tmp/x"}})
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/x" {
		t.Fatalf("got %q", got)
	}
}

func TestResolveBinary_DefaultsToTerraform(t *testing.T) {
	if b := ResolveBinary(provider.Request{}); b != "terraform" {
		t.Fatalf("default should be terraform, got %q", b)
	}
	override := provider.Request{Extra: map[string]string{"tf_bin": "/opt/tf/tofu"}}
	if b := ResolveBinary(override); b != "/opt/tf/tofu" {
		t.Fatalf("override ignored: got %q", b)
	}
}

func TestRegistry_WiresBothTypes(t *testing.T) {
	r := provider.NewRegistry()
	Register(r)
	types := r.Types()
	has := map[string]bool{}
	for _, t := range types {
		has[t] = true
	}
	if !has["state"] || !has["resource"] {
		t.Fatalf("want both state+resource registered, got %v", types)
	}
	// Sanity: each exposes the facts listed in the types/*.yaml vocabulary.
	for _, f := range []string{"accessible", "locked", "resource_count", "terraform_version"} {
		if !contains(r.Facts("state"), f) {
			t.Errorf("state missing fact %q", f)
		}
	}
	for _, f := range []string{"exists_in_state", "drifted", "last_applied_age", "dependents_count"} {
		if !contains(r.Facts("resource"), f) {
			t.Errorf("resource missing fact %q", f)
		}
	}
}

func contains(xs []string, target string) bool {
	for _, x := range xs {
		if x == target {
			return true
		}
	}
	return false
}
