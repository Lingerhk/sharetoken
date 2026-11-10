package relay

import (
	"github.com/prometheus/client_golang/prometheus"

	"sharetoken/internal/version"
)

// Collector exports the relay's state as sharetoken_relay_* metrics, read from
// Stats on every scrape. scripts/relay/monitor.py alerts on these names.
func (r *Relay) Collector() prometheus.Collector { return collector{r} }

type collector struct{ r *Relay }

var (
	clientsDesc = prometheus.NewDesc("sharetoken_relay_network_clients",
		"Devices holding a reservation, by the client build their portal-signed pass names.", []string{"network", "version", "os", "arch"}, nil)
	agentsDesc = prometheus.NewDesc("sharetoken_relay_agents",
		"Connected peers by the build they announce in libp2p identify (unsigned).", []string{"program", "version", "os", "arch"}, nil)
)

func (c collector) Describe(ch chan<- *prometheus.Desc) { prometheus.DescribeByCollect(c, ch) }

func (c collector) Collect(ch chan<- prometheus.Metric) {
	r, st := c.r, c.r.Stats()
	emit := func(t prometheus.ValueType, name, help string, v float64, labels ...string) {
		var names []string
		if len(labels) == 2 {
			names, labels = labels[:1], labels[1:]
		}
		ch <- prometheus.MustNewConstMetric(prometheus.NewDesc("sharetoken_relay_"+name, help, names, nil), t, v, labels...)
	}
	gauge := func(name, help string, v float64) { emit(prometheus.GaugeValue, name, help, v) }
	counter := func(name, help string, v int64) { emit(prometheus.CounterValue, name, help, float64(v)) }

	emit(prometheus.GaugeValue, "info", "Always 1; the label carries the relay's peer ID.", 1, "peer_id", r.Host.ID().String())
	emit(prometheus.GaugeValue, "build_info", "Always 1; the label carries the relay's release.", 1, "version", version.Version)
	enabled := 0.0
	if st.ServiceEnabled {
		enabled = 1
	}
	gauge("service_enabled", "1 while the circuit relay service is running.", enabled)
	gauge("peers", "Connected peers.", float64(len(r.Host.Network().Peers())))
	gauge("connections", "Open connections.", float64(len(r.Host.Network().Conns())))
	gauge("max_connections", "Connection manager high watermark.", float64(r.cfg.MaxConns))
	gauge("reservations", "Reservations held now.", float64(st.Reservations))
	gauge("max_reservations", "Maximum reservations held at once.", float64(r.cfg.MaxReservations))
	gauge("circuits", "Circuits open now.", float64(st.Circuits))
	counter("reservations_granted_total", "Reservations granted, including renewals.", st.ReservationsGranted)
	counter("reservations_denied_total", "Reservation requests denied.", st.ReservationsDenied)
	counter("circuits_opened_total", "Circuits opened.", st.CircuitsOpened)
	counter("circuits_denied_total", "Circuit requests denied.", st.CircuitsDenied)
	counter("relayed_bytes_total", "Bytes forwarded through circuits.", st.BytesRelayed)
	counter("passes_accepted_total", "Relay passes accepted.", st.PassesAccepted)
	counter("passes_rejected_total", "Relay passes rejected.", st.PassesRejected)

	for _, n := range st.Networks {
		emit(prometheus.GaugeValue, "network_reservations", "Reservations held by this network, its coordinator's included.", float64(n.Reservations), "network", n.Name)
		emit(prometheus.GaugeValue, "network_max_reservations", "This network's reservation cap; 0 means only the global limit.", float64(n.MaxReservations), "network", n.Name)
		emit(prometheus.GaugeValue, "network_coordinators_reserved", "Allow-listed peers holding a live reservation.", float64(n.Coordinators), "network", n.Name)
		emit(prometheus.GaugeValue, "network_coordinators_expected", "Allow-listed peers configured.", float64(n.CoordinatorsExpected), "network", n.Name)
		emit(prometheus.CounterValue, "network_passes_accepted_total", "Passes accepted for this network.", float64(n.PassesAccepted), "network", n.Name)
		emit(prometheus.CounterValue, "network_reservations_denied_total", "Reservations denied over this network's limits.", float64(n.ReservationsDenied), "network", n.Name)
		emit(prometheus.GaugeValue, "network_client_mismatches", "Devices holding a reservation whose identify agent disagrees with their pass.", float64(n.ClientMismatches), "network", n.Name)
		for _, c := range n.Clients {
			ch <- prometheus.MustNewConstMetric(clientsDesc, prometheus.GaugeValue, float64(c.Devices), n.Name, c.Version, c.OS, c.Arch)
		}
	}
	for _, a := range st.Agents {
		ch <- prometheus.MustNewConstMetric(agentsDesc, prometheus.GaugeValue, float64(a.Peers), a.Program, a.Version, a.OS, a.Arch)
	}
}
