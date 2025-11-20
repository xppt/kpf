package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/portforward"
	"k8s.io/client-go/transport/spdy"
	cmdutil "k8s.io/kubectl/pkg/cmd/util"
)

type PortFwCfg struct {
	CmdFactory cmdutil.Factory
	Ns string
	PodName string
	Port int
}

type PortFwSession struct {
	BindedPort int

	ctrlBlock *ctrlBlock
}

func StartPortFw(ctx context.Context, cfg *PortFwCfg) (*PortFwSession, error) {
	ctrlBlock := ctrlBlock{
		doneChan: make(chan error, 1),
		readyChan: make(chan struct{}),
		stopReqChan: make(chan struct{}),
	}

	pf := preparePf(ctx, cfg, &ctrlBlock)

	go func() {
		ctrlBlock.doneChan <- pf.ForwardPorts()
		close(ctrlBlock.doneChan)
	}()

	select {
	case <-ctrlBlock.readyChan:
		return &PortFwSession{
			BindedPort: getBindedPort(pf),
			ctrlBlock: &ctrlBlock,
		}, nil
	case forwardError := <-ctrlBlock.doneChan:
		if forwardError == nil {
			return nil, errors.New("ForwardPorts early finish")
		}
		return nil, forwardError
	case <-ctx.Done():
		close(ctrlBlock.stopReqChan)
		<-ctrlBlock.doneChan
		return nil, ctx.Err()
	}
}

func (self *PortFwSession) Close() {
	close(self.ctrlBlock.stopReqChan)
	<-self.ctrlBlock.doneChan
}

type ctrlBlock struct {
	doneChan chan error
	readyChan chan struct{}
	stopReqChan chan struct{}
}

func preparePf(ctx context.Context, cfg *PortFwCfg, ctrlBlock *ctrlBlock) *portforward.PortForwarder {
	clientSet, err := cfg.CmdFactory.KubernetesClientSet()
	dieOnError(err, "failed to get client set")
	restClient, err := cfg.CmdFactory.RESTClient()
	dieOnError(err, "failed to get rest client")
	restConfig, err := cfg.CmdFactory.ToRESTConfig()
	dieOnError(err, "failed to get rest client")

	pod, err := clientSet.CoreV1().Pods(cfg.Ns).Get(ctx, cfg.PodName, metav1.GetOptions{})
	dieOnError(err, "failed to read pod")

	if pod.Status.Phase != corev1.PodRunning {
		err = fmt.Errorf("status=%v", pod.Status.Phase)
		dieOnError(err, "pod is not running")
	}

	pfReq := restClient.Post().Resource("pods").
		Namespace(cfg.Ns).Name(cfg.PodName).SubResource("portforward")

	dialer, err := createDialer("POST", pfReq.URL(), restConfig)
	dieOnError(err, "failed to create dialer")

	ports := []string{fmt.Sprintf("%v:%v", 0, cfg.Port)}

	pf, err := portforward.New(dialer, ports, ctrlBlock.stopReqChan, ctrlBlock.readyChan, nil, os.Stderr)
	dieOnError(err, "failed to create pf")

	return pf
}

func createDialer(method string, url *url.URL, restConfig *rest.Config) (httpstream.Dialer, error) {
	transport, upgrader, err := spdy.RoundTripperFor(restConfig)
	if err != nil {
		return nil, err
	}

	dialer := spdy.NewDialer(upgrader, &http.Client{Transport: transport}, method, url)
	if !cmdutil.PortForwardWebsockets.IsDisabled() {
		tunnelingDialer, err := portforward.NewSPDYOverWebsocketDialer(url, restConfig)
		if err != nil {
			return nil, err
		}

		// websocket dialer or fallback to spdy
		dialer = portforward.NewFallbackDialer(tunnelingDialer, dialer, func(err error) bool {
			return httpstream.IsUpgradeFailure(err) || httpstream.IsHTTPSProxyError(err)
		})
	}

	return dialer, nil
}

func getBindedPort(pf *portforward.PortForwarder) int {
	ports, err := pf.GetPorts()
	dieOnError(err, "failed to GetPorts")

	if len(ports) != 1 {
		dieOnError(errors.New("unexpected GetPorts result"), "")
	}

	return int(ports[0].Local)
}
