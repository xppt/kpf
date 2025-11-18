package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
)

func kubeConfig() (string, *rest.Config) {
	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		clientcmd.NewDefaultClientConfigLoadingRules(), nil)

	ns, _, err := cfg.Namespace()
	dieOnError(err, "failed getting namespace")

	restCfg, err := cfg.ClientConfig()

	return ns, restCfg
}

func main() {
	nsFlag := flag.String("n", "", "")
	flag.Parse()

	if len(flag.Args()) < 3 {
		log.Fatal("usage: kpf [-n ns] pod port prog...")
	}

	pod := flag.Args()[0]
	remotePort := flag.Args()[1]
	progTemplate := flag.Args()[2:]

	namespace, cfg := kubeConfig()

	client, err := kubernetes.NewForConfig(cfg)
	dieOnError(err, "failed creating client")

	effectiveNs := *nsFlag
	if effectiveNs == "" {
		effectiveNs = namespace
	}

	pfReq := client.CoreV1().RESTClient().Post().Namespace(effectiveNs).
		Resource("pods").Name(pod).SubResource("portforward")

	dieOnError(pfReq.Error(), "bad req params")

	roundTripper, upgrader, err := spdy.RoundTripperFor(cfg)
	dieOnError(err, "failed creating roundtripper")

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: roundTripper}, "POST", pfReq.URL())

	ready := make(chan struct{})
	stop := make(chan struct{})

	ports := []string{fmt.Sprintf("%v:%s", 0, remotePort)}

	pf, err := portforward.New(dialer, ports, stop, ready, nil, os.Stderr)
	dieOnError(err, "failed creating port forwarder")
	defer pf.Close()

	go func() {
		<-ready

		ports, err := pf.GetPorts()
		dieOnError(err, "failed to GetPorts")

		if len(ports) != 1 {
			log.Fatal("unexpected GetPorts result")
		}

		localPort := ports[0].Local

		progArgs := expandProgTemplate(progTemplate, fmt.Sprintf("%v", localPort))

		cmd := exec.Command(progArgs[0], progArgs[1:]...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err = cmd.Start()
		dieOnError(err, "unable to start command")

		cmd.Wait()

		if cmd.ProcessState.Exited() {
			os.Exit(cmd.ProcessState.ExitCode())
		} else {
			log.Printf("command abnormal finish: %s", cmd.ProcessState)
			os.Exit(1)
		}
	}()

	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	// TODO: forward the signal to the child if started, cancel otherwise.

	go func() {
		<-signalChan
		close(stop)
	}()

	err = pf.ForwardPorts()
	dieOnError(err, "failed ForwardPorts")
}

func expandProgTemplate(prog []string, param string) []string {
	var result []string
	for index, progArg := range prog {
		if progArg == "_" {
			result = append(result, param)
			result = append(result, prog[index + 1:]...)
			return result
		} else {
			result = append(result, progArg)
		}
	}
	return result
}

func ensureNotEmpty(value string) error {
	if value == "" {
		return fmt.Errorf("empty")
	}
	return nil
}

func dieOnError(err error, msg string) {
	if err != nil {
		log.Printf("%s: %v\n", msg, err)
		os.Exit(1)
	}
}
