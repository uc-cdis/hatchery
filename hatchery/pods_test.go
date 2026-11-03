package hatchery

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"strings"
	"testing"

	k8sv1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestBuildPodFromJSON(t *testing.T) {
	defer SetupAndTeardownTest()()
	expectedApps := 8

	config, err := LoadConfig("../testData/testConfig.json", nil)
	if nil != err {
		t.Errorf("failed to load config, got: %v", err)
		return
	}
	numApps := len(config.Config.Containers)
	if numApps != expectedApps {
		t.Errorf("did not load %d apps, got: %v", expectedApps, numApps)
		return
	}
	app := &config.Config.Containers[numApps-3]
	pod, err := buildPod(config, app, "frickjack", nil)

	if nil != err {
		t.Errorf("failed to build a pod - %v", err)
	}

	numContainers := len(pod.Spec.Containers)
	if numContainers != len(app.Friends)+1 { // +1 b/c main; the sidecar is an init container
		t.Errorf("unexpected number of containers in pod, desired value is %v but got %v", len(app.Friends)+1, numContainers)
	}
	assertNativeSidecar(t, pod, "fuse-container")

	// The configured command runs under a wrapper that exits on SIGTERM.
	fuse := findContainer(pod.Spec.InitContainers, "fuse-container")
	wantArgs := append(append([]string{}, config.Config.Sidecar.Command...), config.Config.Sidecar.Args...)
	if len(fuse.Command) != 4 || fuse.Command[0] != "/bin/sh" || !strings.Contains(fuse.Command[2], "trap") {
		t.Errorf("expected the fuse sidecar command to be wrapped, got %v", fuse.Command)
	}
	if strings.Join(fuse.Args, " ") != strings.Join(wantArgs, " ") {
		t.Errorf("expected the configured command as wrapper args, got %v want %v", fuse.Args, wantArgs)
	}
	jsBytes, err := json.MarshalIndent(pod, "", "  ")
	if nil != err {
		t.Errorf("failed to marshal JSON - %v", err)
	}

	config.Logger.Printf("pod_test marshalled pod: %v", string(jsBytes))
}

func TestBuildPodFromDockstore(t *testing.T) {
	defer SetupAndTeardownTest()()
	expectedApps := 8

	config, err := LoadConfig("../testData/testConfig.json", nil)
	if nil != err {
		t.Errorf("failed to load config, got: %v", err)
		return
	}
	numApps := len(config.Config.Containers)
	if numApps != expectedApps {
		t.Errorf("did not load %d apps, got: %v", expectedApps, numApps)
		return
	}
	app := &config.Config.Containers[numApps-2]
	pod, err := buildPod(config, app, "frickjack", nil)

	if nil != err {
		t.Errorf("failed to build a pod - %v", err)
	}

	numContainers := len(pod.Spec.Containers)
	if numContainers != len(app.Friends) { // the sidecar is an init container
		t.Errorf("unexpected number of containers in pod, desired value is %v but got %v", len(app.Friends), numContainers)
	}
	assertNativeSidecar(t, pod, "fuse-container")
	jsBytes, err := json.MarshalIndent(pod, "", "  ")
	if nil != err {
		t.Errorf("failed to marshal JSON - %v", err)
	}

	config.Logger.Printf("pod_test marshalled pod: %v", string(jsBytes))
}

// assertNativeSidecar checks that the named container runs as a native sidecar:
// a restartable init container, and not also a regular container.
func assertNativeSidecar(t *testing.T, pod *k8sv1.Pod, name string) {
	t.Helper()
	if c := findContainer(pod.Spec.Containers, name); c != nil {
		t.Errorf("expected %s to not be a regular container", name)
	}
	c := findContainer(pod.Spec.InitContainers, name)
	if c == nil {
		t.Fatalf("expected %s to be an init container", name)
	}
	if c.RestartPolicy == nil || *c.RestartPolicy != k8sv1.ContainerRestartPolicyAlways {
		t.Errorf("expected %s to have restartPolicy Always, got %v", name, c.RestartPolicy)
	}
}

func findContainer(containers []k8sv1.Container, name string) *k8sv1.Container {
	for i := range containers {
		if containers[i].Name == name {
			return &containers[i]
		}
	}
	return nil
}

func TestBuildPodNativeSidecarsDisabled(t *testing.T) {
	defer SetupAndTeardownTest()()

	config, err := LoadConfig("../testData/testConfig.json", nil)
	if nil != err {
		t.Fatalf("failed to load config, got: %v", err)
	}
	config.Config.DisableNativeSidecars = true
	app := &config.Config.Containers[len(config.Config.Containers)-3]
	pod, err := buildPod(config, app, "frickjack", nil)
	if nil != err {
		t.Fatalf("failed to build a pod - %v", err)
	}

	if len(pod.Spec.InitContainers) != 0 {
		t.Errorf("expected no init containers, got %d", len(pod.Spec.InitContainers))
	}
	if len(pod.Spec.Containers) != len(app.Friends)+2 { // +2 b/c sidecar + main
		t.Errorf("unexpected number of containers in pod, desired value is %v but got %v", len(app.Friends)+2, len(pod.Spec.Containers))
	}
	if pod.Spec.Containers[0].Name != "fuse-container" || pod.Spec.Containers[0].RestartPolicy != nil {
		t.Errorf("expected fuse-container first with no restartPolicy, got %s", pod.Spec.Containers[0].Name)
	}
	if strings.Join(pod.Spec.Containers[0].Command, " ") != strings.Join(config.Config.Sidecar.Command, " ") {
		t.Errorf("expected the fuse sidecar command to be left alone, got %v", pod.Spec.Containers[0].Command)
	}
}

func TestApplySquashFSMounter(t *testing.T) {
	// Swap in a fresh config: other tests leave the global in assorted states,
	// and the mounter reads the native-sidecar switch from it.
	defer func(orig *FullHatcheryConfig) { Config = orig }(Config)
	Config = &FullHatcheryConfig{Logger: log.New(io.Discard, "", log.LstdFlags)}

	const ns = "jupyter-pods"
	opts := SquashFSMountConfig{Enabled: true, BucketName: "software-library-bucket"}
	newPod := func() *k8sv1.Pod {
		return &k8sv1.Pod{Spec: k8sv1.PodSpec{Containers: []k8sv1.Container{{
			Name:    "hatchery-container",
			Command: []string{"/jupyterlab-start.sh"},
			Args:    []string{"--port=8888"},
		}}}}
	}

	t.Run("nativeSidecar", func(t *testing.T) {
		Config.Config.DisableNativeSidecars = false
		pod := newPod()
		if err := applySquashFSMounter(context.Background(), fake.NewSimpleClientset().CoreV1(), ns, pod, opts); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		assertNativeSidecar(t, pod, "apps-mounter")
		mounter := findContainer(pod.Spec.InitContainers, "apps-mounter")
		if mounter.StartupProbe == nil || mounter.StartupProbe.Exec == nil {
			t.Fatalf("expected an exec startup probe gating the workspace on the /apps mount")
		}
		if probe := strings.Join(mounter.StartupProbe.Exec.Command, " "); !strings.Contains(probe, "squashfs") {
			t.Errorf("expected the startup probe to check for a squashfs mount, got %q", probe)
		}

		// The workspace keeps its own command and gets no drain hook: kubelet
		// already orders it after the mounter on start and before it on stop.
		ws := findContainer(pod.Spec.Containers, "hatchery-container")
		if strings.Join(ws.Command, " ") != "/jupyterlab-start.sh" || strings.Join(ws.Args, " ") != "--port=8888" {
			t.Errorf("expected the workspace command to be left alone, got %v %v", ws.Command, ws.Args)
		}
		if ws.Lifecycle != nil && ws.Lifecycle.PreStop != nil {
			t.Errorf("expected no drain preStop on the workspace container")
		}
		if !hasVolumeMount(ws, "apps-mount", "/apps") {
			t.Errorf("expected the workspace container to mount /apps")
		}
		if pod.Spec.TerminationGracePeriodSeconds == nil || *pod.Spec.TerminationGracePeriodSeconds != squashFSTerminationGracePeriod {
			t.Errorf("expected termination grace period %d, got %v", squashFSTerminationGracePeriod, pod.Spec.TerminationGracePeriodSeconds)
		}
	})

	t.Run("regularContainer", func(t *testing.T) {
		Config.Config.DisableNativeSidecars = true
		pod := newPod()
		if err := applySquashFSMounter(context.Background(), fake.NewSimpleClientset().CoreV1(), ns, pod, opts); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(pod.Spec.InitContainers) != 0 {
			t.Errorf("expected no init containers, got %d", len(pod.Spec.InitContainers))
		}
		if pod.Spec.Containers[0].Name != "apps-mounter" {
			t.Fatalf("expected apps-mounter as the first container, got %s", pod.Spec.Containers[0].Name)
		}
		if pod.Spec.Containers[0].StartupProbe != nil {
			t.Errorf("expected no startup probe on a regular container: it would not gate the workspace")
		}

		// Without ordering guarantees the workspace waits for the mount itself and
		// lingers on stop so the mounter can unmount first.
		ws := findContainer(pod.Spec.Containers, "hatchery-container")
		if len(ws.Args) < 2 || ws.Args[0] != squashFSWrapperScript || ws.Args[1] != "hatchery-wrapper" {
			t.Errorf("expected the workspace command to be wrapped, got %v", ws.Args)
		}
		if ws.Lifecycle == nil || ws.Lifecycle.PreStop == nil || ws.Lifecycle.PreStop.Exec == nil {
			t.Fatalf("expected a drain preStop on the workspace container")
		}
		if !hasVolumeMount(ws, "apps-mount", "/apps") {
			t.Errorf("expected the workspace container to mount /apps")
		}
	})
}

func hasVolumeMount(c *k8sv1.Container, name, path string) bool {
	for _, m := range c.VolumeMounts {
		if m.Name == name && m.MountPath == path {
			return true
		}
	}
	return false
}

func TestRemoveFinishedPod(t *testing.T) {
	defer func(orig *FullHatcheryConfig) { Config = orig }(Config)
	Config = &FullHatcheryConfig{Logger: log.New(io.Discard, "", log.LstdFlags)}
	Config.Config.UserNamespace = "jupyter-pods"

	const podName = "hatchery-bob"
	podInPhase := func(phase k8sv1.PodPhase) *k8sv1.Pod {
		return &k8sv1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: "jupyter-pods"},
			Status:     k8sv1.PodStatus{Phase: phase},
		}
	}

	t.Run("noPod", func(t *testing.T) {
		client := fake.NewSimpleClientset().CoreV1()
		if err := removeFinishedPod(context.Background(), client, podName, "bob", ""); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	for _, phase := range []k8sv1.PodPhase{k8sv1.PodSucceeded, k8sv1.PodFailed} {
		t.Run("removes"+string(phase), func(t *testing.T) {
			client := fake.NewSimpleClientset(podInPhase(phase)).CoreV1()
			if err := removeFinishedPod(context.Background(), client, podName, "bob", ""); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if _, err := client.Pods("jupyter-pods").Get(context.Background(), podName, metav1.GetOptions{}); !k8serrors.IsNotFound(err) {
				t.Errorf("expected the %s pod to be deleted, got %v", phase, err)
			}
		})
	}

	for _, phase := range []k8sv1.PodPhase{k8sv1.PodPending, k8sv1.PodRunning} {
		t.Run("keeps"+string(phase), func(t *testing.T) {
			client := fake.NewSimpleClientset(podInPhase(phase)).CoreV1()
			if err := removeFinishedPod(context.Background(), client, podName, "bob", ""); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if _, err := client.Pods("jupyter-pods").Get(context.Background(), podName, metav1.GetOptions{}); err != nil {
				t.Errorf("expected the %s pod to be left alone, got %v", phase, err)
			}
		})
	}
}
