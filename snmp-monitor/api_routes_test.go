package main

import "testing"

func TestRouteColumnOIDs(t *testing.T) {
	// RFC 4292 / RFC 2096 列号; 曾把 Metric2..5(-1)当成协议/年龄/状态读取。
	cases := map[string]string{
		oidInetCidrRouteStatus: ".1.3.6.1.2.1.4.24.7.1.17",
		oidIpCidrRouteType:     ".1.3.6.1.2.1.4.24.4.1.6",
		oidIpCidrRouteProto:    ".1.3.6.1.2.1.4.24.4.1.7",
		oidIpCidrRouteAge:      ".1.3.6.1.2.1.4.24.4.1.8",
		oidIpCidrRouteMetric1:  ".1.3.6.1.2.1.4.24.4.1.11",
		oidIpCidrRouteStatus:   ".1.3.6.1.2.1.4.24.4.1.16",
		oidIpRouteAge:          ".1.3.6.1.2.1.4.21.1.10",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("OID = %s, want %s", got, want)
		}
	}
}

func routeCols(ints map[string]int64) routeColumns {
	c := routeColumns{ints: map[string]map[string]int64{}}
	for k, v := range ints {
		c.ints[k] = map[string]int64{"i": v}
	}
	return c
}

func TestNewRouteEntryDecoding(t *testing.T) {
	e := newRouteEntry("inetCidrRouteTable", "10.0.0.0/8", "0.0.0.0", 3,
		routeCols(map[string]int64{"type": 4, "proto": 13, "age": 120, "metric": 2}), "i", nil)
	if e.Protocol != "OSPF" || e.Type != "远端" || e.NextHop != "-" || e.Age != 120 || e.Metric != 2 || !e.enumValid() {
		t.Fatalf("unexpected entry: %+v", e)
	}

	e = newRouteEntry("ipCidrRouteTable", "10.0.0.0/8", "1.1.1.1", 3,
		routeCols(map[string]int64{"type": -1, "proto": -1, "age": 4294967295, "metric": -1}), "i", nil)
	if e.Protocol != "未上报" || e.Age != -1 || e.Metric != -1 || e.enumValid() {
		t.Fatalf("invalid values should be unreported: %+v", e)
	}

	e = newRouteEntry("ipRouteTable", "10.0.0.0/8", "-", 3, routeCols(map[string]int64{"type": 3, "proto": 2}), "i", nil)
	if e.Type != "直连" || e.Protocol != "本地" || e.Age != -1 || e.Metric != -1 {
		t.Fatalf("legacy decode: %+v", e)
	}

	e = newRouteEntry("ipv6RouteTable", "2001:db8::/32", "-", 3, routeCols(map[string]int64{"type": 4, "proto": 6}), "i", nil)
	if e.Protocol != "OSPFv3" || e.Type != "远端" {
		t.Fatalf("ipv6 decode: %+v", e)
	}
}

func TestRepairRouteEnums(t *testing.T) {
	rows := []RouteEntry{
		newRouteEntry("inetCidrRouteTable", "10.1.0.0/16", "192.168.1.2", 3, routeCols(map[string]int64{"type": 4, "proto": -1}), "i", nil),
		newRouteEntry("inetCidrRouteTable", "10.2.0.0/16", "192.168.1.3", 3, routeCols(map[string]int64{"type": 4, "proto": 14}), "i", nil),
	}
	legacy := []RouteEntry{
		newRouteEntry("ipRouteTable", "10.1.0.0/16", "192.168.1.9", 3, routeCols(map[string]int64{"type": 4, "proto": 13}), "i", nil),
		newRouteEntry("ipRouteTable", "10.2.0.0/16", "192.168.1.3", 3, routeCols(map[string]int64{"type": 4, "proto": 13}), "i", nil),
	}
	if n := repairRouteEnums(rows, legacy); n != 1 {
		t.Fatalf("repaired = %d, want 1", n)
	}
	if rows[0].Protocol != "OSPF" || rows[0].ProtocolEnum != "ipRouteTable" || rows[0].Type != "远端" || rows[0].TypeEnum != "inetCidrRouteTable" {
		t.Fatalf("row 0 not repaired correctly: %+v", rows[0])
	}
	if rows[1].Protocol != "BGP" {
		t.Fatalf("valid protocol must not be overwritten: %+v", rows[1])
	}
}

func TestParseInetRouteIndex(t *testing.T) {
	// dest 1.4.10.0.0.0 / pfx 8 / policy 2.0.0 / nexthop 1.4.192.168.1.1
	dst, nh, ok := parseInetRouteIndex("1.4.10.0.0.0.8.2.0.0.1.4.192.168.1.1")
	if !ok || dst != "10.0.0.0/8" || nh != "192.168.1.1" {
		t.Fatalf("got %q %q %v", dst, nh, ok)
	}
}

func TestSortRouteEntriesNumeric(t *testing.T) {
	rows := []RouteEntry{{Destination: "2001:db8::/32"}, {Destination: "10.0.0.0/8"}, {Destination: "9.0.0.0/8"}, {Destination: "10.0.0.0/16"}}
	sortRouteEntries(rows)
	want := []string{"9.0.0.0/8", "10.0.0.0/8", "10.0.0.0/16", "2001:db8::/32"}
	for i, w := range want {
		if rows[i].Destination != w {
			t.Fatalf("order[%d] = %s, want %s", i, rows[i].Destination, w)
		}
	}
}

func TestTransceiverMedium(t *testing.T) {
	cases := []struct {
		typ, conn string
		want      int
	}{
		{"1000BASE-GT-SFP", "", 1},
		{"Mini-GBIC-GT", "", 1},
		{"1000BASE-T", "RJ45", 1},
		{"10GBASE-T", "", 1},
		{"", "RJ-45", 1},
		{"XG-SFP-CU1M", "Copper pigtail", 1},
		{"1000BASE-LX", "LC", 2},
		{"Mini-GBIC-LX-SM1310", "", 2},
		{"XG-SFP-SR-MM850", "LC", 2},
		{"GE-SFP-LX20-SM1310-BIDI", "", 2},
		{"1000BASE-T", "LC", 0},
		{"", "", 0},
		{"unknown", "", 0},
	}
	for _, c := range cases {
		if got := transceiverMedium(c.typ, c.conn); got != c.want {
			t.Errorf("transceiverMedium(%q,%q) = %d, want %d", c.typ, c.conn, got, c.want)
		}
	}
}
