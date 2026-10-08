/*
 * 导出端口 CSV。
 */
/* ---------- 导出 ---------- */
function csvCell(v) { return '"' + String(v === null || v === undefined ? '' : v).replace(/"/g, '""') + '"'; }
function exportCSV() {
  var d = currentDetail;
  if (!d) { return; }
  var csv = '\uFEFF端口,描述,层级,VLAN模式,VLAN(access/native),允许VLAN(tagged),Untagged VLAN,IP,聚合组,状态,速率(M),双工,上联口,LLDP对端设备,LLDP对端端口,LLDP对端IP,收光Rx,发光Tx,入向Mbps,入向%,出向Mbps,出向%,增量错包,学习MAC数\n';
  (d.ports || []).forEach(function (p) {
    csv += [p.name, p.alias, p.layer, p.vlan_mode, p.vlan_id, p.vlan_tagged, p.vlan_untagged, p.ip_addr, p.agg_group, p.status, p.speed_mbps, p.duplex, p.is_uplink ? '是' : '否', p.lldp_remote_dev, p.lldp_remote_port, p.lldp_remote_ip || '',
      p.optical_rx_power, p.optical_tx_power, p.in_speed_mbps.toFixed(2), p.in_usage_percent.toFixed(1),
      p.out_speed_mbps.toFixed(2), p.out_usage_percent.toFixed(1), p.delta_errors, p.mac_count].map(csvCell).join(',') + '\n';
  });
  var link = document.createElement('a');
  link.href = URL.createObjectURL(new Blob([csv], { type: 'text/csv;charset=utf-8;' }));
  link.setAttribute('download', d.sys_name + '_' + d.ip + '_端口清单.csv');
  document.body.appendChild(link); link.click(); document.body.removeChild(link);
}
