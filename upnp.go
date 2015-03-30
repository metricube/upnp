//upnp functions to open ports on router
package upnp

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	BroadcastRetryCount      = 3
	BroadcastWaitTimeSeconds = 3
	ServiceType              = "urn:schemas-upnp-org:service:WANIPConnection:1"
)

// Device Description xml elements
type Service struct {
	ServiceType string `xml:"serviceType"`
	//ServiceId   string `xml:"serviceId"`
	//SCPDURL     string `xml:"SCPDURL"`
	ControlURL string `xml:"controlURL"`
	//EventSubURL string `xml:"eventSubURL"`
}

type UPNP struct {
	Gateway *Gateway
}

type Gateway struct {
	GatewayName   string
	Host          string
	DeviceDescUrl string
	Cache         string
	ST            string
	ControlURL    string
	OutsideIP     net.IP
}

// NewUPNP returns a new UPNP object with a populated Gateway object.
func NewUPNP() (*UPNP, error) {
	u := &UPNP{}

	// Populate te the Gateway fields
	err := u.findGateway()
	if err != nil {
		return nil, err
	}

	err = u.DeviceDesc()
	if err != nil {
		return nil, err
	}

	if len(u.Gateway.ControlURL) == 0 || len(u.Gateway.Host) == 0 {
		return nil, errors.New("upnp: could not get gateway control url or host")
	}

	return u, nil
}

// perform the requested upnp action
func (u *UPNP) perform(action, body string) (*http.Response, error) {
	// Add soap envelope
	envelope := `<?xml version="1.0"?>
<SOAP-ENV:Envelope 
	xmlns:SOAP-ENV="http://schemas.xmlsoap.org/soap/envelope/" 
	SOAP-ENV:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">
 <SOAP-ENV:Body>
 ` + body + "</SOAP-ENV:Body></SOAP-ENV:Envelope>\r\n\r\n"

	header := http.Header{}
	header.Set("SOAPAction", action)
	header.Set("Content-Type", "text/xml")
	header.Set("Connection", "Close")
	header.Set("Content-Length", string(len(envelope)))

	url := "http://" + u.Gateway.Host + u.Gateway.ControlURL
	req, _ := http.NewRequest("POST", url, strings.NewReader(envelope))
	req.Header = header

	//dumpreq, _ := httputil.DumpRequestOut(req, true)
	//log.Println(string(dumpreq))

	resp, err := http.DefaultClient.Do(req)

	//dumpresp, _ := httputil.DumpResponse(resp, true)
	//log.Println(string(dumpresp))

	return resp, err
}

// AddPortMapping to the WAN/Internet
func (u *UPNP) AddPortMapping(localPort, remotePort int, protocol string) error {

	protocol = strings.ToUpper(protocol)
	if protocol != "UDP" && protocol != "TCP" {
		return errors.New("upnp: bad protocol supplied: " + protocol)
	}

	action := `"urn:schemas-upnp-org:service:WANIPConnection:1#AddPortMapping"`

	body := fmt.Sprintf(`<m:AddPortMapping xmlns:m="%v">
    <NewRemoteHost></NewRemoteHost>
    <NewExternalPort>%v</NewExternalPort>
	<NewProtocol>%v</NewProtocol>
	<NewInternalPort>%v</NewInternalPort>
	<NewInternalClient>%v</NewInternalClient>
	<NewEnabled>1</NewEnabled>
	<NewPortMappingDescription>USATVA</NewPortMappingDescription>
	<NewLeaseDuration>0</NewLeaseDuration>
  </m:AddPortMapping>`, ServiceType, remotePort, protocol, localPort, GetLocalAddress())

	r, err := u.perform(action, body)
	if err != nil {
		return fmt.Errorf("upnp: bad response from gateway for add port mapping (%v)", err)
	}

	if r.StatusCode == 200 {
		fmt.Printf("upnp: added mapping wan:%v => %v %v\n", remotePort, localPort, protocol)
		return nil
	}

	return errors.New("upnp: bad response for add port")
}

func (u *UPNP) DelPortMapping(remotePort int, protocol string) error {

	protocol = strings.ToUpper(protocol)
	if protocol != "UDP" && protocol != "TCP" {
		return errors.New("upnp: bad protocol supplied: " + protocol)
	}

	action := `"urn:schemas-upnp-org:service:WANIPConnection:1#DeletePortMapping"`

	body := fmt.Sprintf(`<m:DeletePortMapping xmlns:m="urn:schemas-upnp-org:service:WANIPConnection:1">
	<NewRemoteHost></NewRemoteHost>
	<NewExternalPort>%v</NewExternalPort>
	<NewProtocol>%v</NewProtocol>
  </m:DeletePortMapping>`, remotePort, protocol)

	r, err := u.perform(action, body)
	if err != nil {
		return fmt.Errorf("upnp: bad response from gateway for del port mapping (%v)", err)
	}

	if r.StatusCode == 200 {
		fmt.Printf("upnp: removed mapping wan:%v %v\n", remotePort, protocol)
		return nil
	}

	return errors.New("upnp: bad response for del port mapping")
}

func (u *UPNP) ExternalIPAddress() (net.IP, error) {
	action := `"urn:schemas-upnp-org:service:WANIPConnection:1#GetExternalIPAddress"`
	body := `<m:GetExternalIPAddress xmlns:m="urn:schemas-upnp-org:service:WANIPConnection:1"/>`

	r, err := u.perform(action, body)
	if r.StatusCode != 200 {
		return nil, fmt.Errorf("upnp: bad response from gateway for get external ip address (%v:%v)", r.StatusCode, err)
	}

	decoder := xml.NewDecoder(r.Body)
	found := false
	for t, err := decoder.Token(); err == nil; t, err = decoder.Token() {
		switch se := t.(type) {
		case xml.StartElement:
			found = (se.Name.Local == "NewExternalIPAddress")
		case xml.CharData:
			if found {
				ip := net.ParseIP(string(se))
				u.Gateway.OutsideIP = ip
				return ip, nil
			}
		}
	}

	return nil, errors.New("upnp: could not get public ip from gateway")
}

func (u *UPNP) DeviceDesc() error {
	header := http.Header{}
	header.Set("Host", u.Gateway.Host)
	header.Set("Connection", "keep-alive")

	request, _ := http.NewRequest("GET", "http://"+u.Gateway.Host+u.Gateway.DeviceDescUrl, nil)
	request.Header = header

	response, err := http.DefaultClient.Do(request)

	if response.StatusCode != 200 {
		return err
	}

	decoder := xml.NewDecoder(response.Body)
	for t, err := decoder.Token(); err == nil; t, err = decoder.Token() {
		switch se := t.(type) {
		case xml.StartElement:
			if se.Name.Local == "service" {
				var s Service
				if err := decoder.DecodeElement(&s, &se); err != nil {
					continue
				}
				if s.ServiceType == ServiceType {
					u.Gateway.ControlURL = s.ControlURL
					return nil
				}
			}
		}
	}
	return errors.New("upnp: Can't get control url for gateway")
}

func (u *UPNP) findGateway() error {

	search := "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"ST: urn:schemas-upnp-org:service:WANIPConnection:1\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 3\r\n\r\n"

	// Listening port for response
	localAddr, err := net.ResolveUDPAddr("udp", ":0")
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	buf := make([]byte, 1024)
	var result string
	// Broadcast message
	for i := 0; i < BroadcastRetryCount; i++ {
		remotAddr, err := net.ResolveUDPAddr("udp", "239.255.255.250:1900")
		_, err = conn.WriteToUDP([]byte(search), remotAddr)
		if err != nil {
			return err
		}

		conn.SetReadDeadline(time.Now().Add(BroadcastWaitTimeSeconds * time.Second))
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if e, ok := err.(net.Error); ok && e.Timeout() {
				continue
			}
			return err
		}
		result = string(buf[:n])
		break
	}

	// Populate Gateway Info
	u.Gateway = &Gateway{}

	lines := strings.Split(result, "\r\n")
	for _, line := range lines {
		nameValues := strings.SplitAfterN(line, ":", 2)
		if len(nameValues) < 2 {
			continue
		}
		switch strings.ToUpper(strings.Trim(strings.Split(nameValues[0], ":")[0], " ")) {
		case "ST":
			u.Gateway.ST = nameValues[1]
		case "CACHE-CONTROL":
			u.Gateway.Cache = nameValues[1]
		case "LOCATION":
			urls := strings.Split(strings.Split(nameValues[1], "//")[1], "/")
			u.Gateway.Host = urls[0]
			u.Gateway.DeviceDescUrl = "/" + urls[1]
		case "SERVER":
			u.Gateway.GatewayName = nameValues[1]
		}
	}

	return nil
}

func GetLocalAddress() net.IP {
	addrs, _ := net.InterfaceAddrs()
	for _, addr := range addrs {
		ip, _, err := net.ParseCIDR(addr.String())

		if err != nil {
			continue
		}

		if isPrivateUnicast(ip) {
			return ip
		}
	}

	return nil
}

func isPrivateUnicast(ip net.IP) bool {
	for _, v := range []string{"10.0.0.0/8", "192.168.0.0/16", "172.16.0.0/12"} {
		_, block, _ := net.ParseCIDR(v)
		if block.Contains(ip) {
			return true
		}
	}

	return false
}
