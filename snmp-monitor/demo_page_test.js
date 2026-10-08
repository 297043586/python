// 演示服务注入的脚本(见 demo_test.go): 收集 JS 错误, 按 ?demo=xxx 自动操作页面, 结果回报给服务端。
(function () {
  var errs = [], info = [];
  window.addEventListener('error', function (e) { errs.push('ERR ' + e.message + ' @' + (e.filename || '') + ':' + (e.lineno || '') + ':' + (e.colno || '')); });
  window.addEventListener('unhandledrejection', function (e) { errs.push('REJ ' + (e.reason && (e.reason.stack || e.reason))); });
  var oe = console.error; console.error = function () { errs.push('CE ' + Array.prototype.join.call(arguments, ' ')); oe.apply(console, arguments); };
  var ow = console.warn; console.warn = function () { errs.push('CW ' + Array.prototype.join.call(arguments, ' ')); ow.apply(console, arguments); };
  var q = new URLSearchParams(location.search), act = q.get('demo') || '';
  window.demoInfo = function (x) { info.push(x); };
  function report(tag) {
    var x = new XMLHttpRequest();
    x.open('POST', '/demo/report', false);
    try { x.send(JSON.stringify({ act: act, tag: tag, errs: errs, info: info })); } catch (e) {}
  }
  function login() {
    return fetch('/api/login', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ user: 'admin', password: 'demo-pass-123' }) })
      .then(function () { return refreshSession(); });
  }
  function later(ms, fn) { return new Promise(function (res) { setTimeout(function () { try { fn && fn(); } catch (e) { errs.push('ACT ' + e.stack); } res(); }, ms); }); }
  var steps = {
    filters: function () {
      openMACTable();
      return later(1200, function () {
        info.push('mac rows all=' + GMAC.t.rows.length);
        msSetValues('gmacVlanFilter', ['10', '20']); renderGlobalMACTable();
        info.push('mac rows vlan10+20=' + GMAC.t.rows.length);
        msSetValues('gmacKindFilter', ['uplink', 'ambiguous']); renderGlobalMACTable();
        info.push('mac rows +kind=' + GMAC.t.rows.length);
        document.querySelector('#gmacVlanFilter .ms-btn').click();
      });
    },
    alerts: function () {
      openAlertTable();
      return later(1200, function () {
        info.push('alert rows default(open)=' + AC.now.rows.length);
        msSetValues('galertDeviceFilter', ['10.0.0.1', '10.0.1.3']); renderGlobalAlertTable();
        info.push('alert rows 2 devices=' + AC.now.rows.length);
        document.querySelector('#galertDeviceFilter .ms-btn').click();
      });
    },
    ports: function () {
      openModal('10.0.1.1');
      return later(1200, function () {
        switchTab(1);
        setFilter('UP'); setFilter('OPT');
        info.push('ports UP+OPT=' + document.querySelectorAll('#portBody tr').length);
        setFilter('DOWN');
        info.push('ports UP|DOWN +OPT=' + document.querySelectorAll('#portBody tr').length);
        jumpPortHosts('GigabitEthernet 0/2');
        info.push('hosts on 0/2=' + document.querySelectorAll('#arpBody tr').length);
      });
    },
    settings: function () {
      return login().then(function () { openSettings(); return later(1000); }).then(function () {
        SET.sec = 6; renderSettings();
        info.push('backup cmds huawei=' + JSON.stringify(SET.cfg.backup_commands.huawei));
      });
    },
    settingsSave: function () {
      return login().then(function () { openSettings(); return later(1000); }).then(function () {
        SET.cfg.ui.site_title = '测试监控平台';
        SET.cfg.check_interval_seconds = 90;
        SET.cfg.web_port = 18181;
        saveSettings();
        return later(1200, function () { info.push('msg=' + byId('setMsg').innerText); SET.sec = 0; renderSettings(); });
      });
    },
    settingsBad: function () {
      return login().then(function () { openSettings(); return later(1000); }).then(function () {
        SET.cfg.check_interval_seconds = 5; SET.cfg.alerts.cpu.threshold = 300;
        saveSettings();
        return later(1000, function () { info.push('msg=' + byId('setMsg').innerText); });
      });
    },
    backup: function () {
      return login().then(function () { openBackups(); return later(1200); }).then(function () {
        editBackupCred(bkState.devices.filter(function (d) { return d.ip === '10.0.1.1'; })[0].device_id);
        info.push('hint=' + byId('bcDefault').innerText);
        byId('bcVendor').value = 'huawei'; bkVendorHint(bkState.devices[0].device_id);
        info.push('hint huawei=' + byId('bcDefault').innerText);
      });
    },
    topo3d: function () {
      try { localStorage.setItem('topo3d', '1'); } catch (e) {}
      T3.on = true;
      openTopology();
      return later(7000, function () {
        info.push('webgl=' + glSupported() + ' TG=' + !!(TG && TG.st) + ' meshes=' + (TG ? Object.keys(TG.meshes).length : 0) + ' links=' + (TG ? TG.linkObjs.length : 0) + ' particles=' + (TG && TG.pdata ? TG.pdata.length : 0));
        if (TG) { Object.keys(TG.meshes).forEach(function (k) { var g = TG.meshes[k]; if (g.userData.members) { info.push(k + ' members=' + g.userData.members.length + ' h=' + Math.round(g.userData.height)); } }); }
      });
    },
    topo3dcanvas: function () {
      try { localStorage.setItem('topo3d', '1'); localStorage.setItem('topo3dEngine', 'canvas'); } catch (e) {}
      T3.on = true; TGE.engine = 'canvas';
      openTopology();
      return later(3000, function () { info.push('canvas=' + !!byId('topo3dCanvas')); });
    },
    dc3d: function () {
      try { localStorage.setItem('rackView', '3d'); } catch (e) {}
      openRacks();
      return later(7000, function () {
        info.push('racks=' + (DC.ctx ? Object.keys(DC.ctx.rackObjs).length : -1) + ' devs=' + (DC.ctx ? Object.keys(DC.ctx.devObjs).length : -1));
        if (DC.ctx) { Object.keys(DC.ctx.devObjs).forEach(function (ip) { var x = DC.ctx.devObjs[ip].userData.slot; info.push(ip + ' ' + x.d.rack + ' U' + x.top + ' h' + x.h); }); }
      });
    },
    dc3dEdit: function () {
      try { localStorage.setItem('rackView', '3d'); } catch (e) {}
      return login().then(function () { openRacks(); return later(6000); }).then(function () {
        dcToggleEdit();
        DC.sel = { t: 'rack', id: 'r1' }; dcRenderSide();
        byId('dcNiName').value = 'DB-01'; byId('dcNiU').value = 10; byId('dcNiH').value = 2;
        dcItemAdd('r1');
        info.push('items=' + DC.layout.items.length + ' dirty=' + DC.dirty);
        dcStartPlace('rack'); dcPlaceNew({ x: 6, z: 3 }); info.push('racks=' + DC.layout.racks.length);
        dcSave();
        return later(2500, function () { info.push('after save dirty=' + DC.dirty + ' racks=' + DC.layout.racks.length); DC.sel = { t: 'rack', id: 'r1' }; dcRenderSide(); dcFlyToRack(dcRackById('r1')); });
      }).then(function () { return later(1500); });
    },
    rack2d: function () {
      try { localStorage.setItem('rackView', '2d'); } catch (e) {}
      openRacks();
      return later(2500, function () { info.push('2d racks=' + document.querySelectorAll('#rackStage .pv-rack').length + ' devs=' + document.querySelectorAll('#rackStage .pv-dev').length + ' ports=' + document.querySelectorAll('#rackStage .pp').length); });
    },
    panel: function () {
      openModal('10.0.0.1');
      return later(1500, function () { switchTab(9); info.push('panel devs=' + document.querySelectorAll('#panelBox .pv-dev').length + ' ports=' + document.querySelectorAll('#panelBox .pp').length); });
    },
    bs: function () {
      var st = q.get('style') || 'tech', center = q.get('center') || 'map';
      try { localStorage.setItem('bsPrefs', JSON.stringify({ style: st, center: center })); } catch (e) {}
      BS.prefs = { style: st, center: center };
      openBigScreen();
      return later(7000, function () {
        var f = byId('bsFeed');
        info.push('feed scrollH=' + f.scrollHeight + ' clientH=' + f.clientHeight + ' top=' + f.scrollTop + ' scrollers=' + BS.scrollers.length + ' raf=' + BS.scrollRaf + ' last=' + BS.scrollLast + ' speed=' + bsPref('scroll_speed') + ' s0=' + JSON.stringify(BS.scrollers.map(function (s) { return [s.el.id, s.hover, Math.round(s.userUntil), Math.round(s.pauseUntil), s.el.scrollTop]; })));
        f.scrollTop = 40;
        info.push('after set top=' + f.scrollTop + ' view=' + BS.view);
      });
    },
    panelCh: function () {
      openModal(q.get('ip') || '10.0.0.2');
      return later(1500, function () { switchTab(9); }).then(function () { return later(1500); }).then(function () {
        info.push('cards=' + document.querySelectorAll('#panelBox .pv-card').length + ' empty=' + document.querySelectorAll('#panelBox .pv-blankcard').length + ' form=' + JSON.stringify(PNL.forms[currentDetail.ip]));
      });
    },
    topo2d: function () {
      try { localStorage.setItem('topo3d', '0'); } catch (e) {}
      T3.on = false;
      openTopology();
      return later(2500, function () {
        info.push('nodes=' + document.querySelectorAll('#tpNodes .tp-node').length + ' links=' + document.querySelectorAll('#tpLinks .tp-link').length + ' exts=' + document.querySelectorAll('#tpExts .tp-ext').length + ' extMode=' + TP.extMode + ' scale=' + TP.view.s.toFixed(2));
        info.push('roles=' + JSON.stringify(TP.roles));
        var l = q.get('layout'); if (l) { tpAutoLayout(l); }
      }).then(function () { return later(1200); });
    },
    topo2dEdit: function () {
      try { localStorage.setItem('topo3d', '0'); } catch (e) {}
      T3.on = false;
      return login().then(function () { openTopology(); return later(2500); }).then(function () {
        toggleTopoEdit();
        // 框选: 模拟在空白处拖一个覆盖整个画面的框
        var svg = byId('topoSvg'), r = svg.getBoundingClientRect();
        var ev = function (type, x, y, t) { (t || svg).dispatchEvent(new MouseEvent(type, { clientX: x, clientY: y, button: 0, bubbles: true })); };
        ev('mousedown', r.left + 3, r.top + 3); ev('mousemove', r.right - 3, r.bottom - 3, window); ev('mouseup', r.right - 3, r.bottom - 3, window);
        info.push('boxSelected=' + tpSelCount());
        tpAlign('vcenter');
        return later(800);
      }).then(function () {
        var ys = Object.keys(TP.sel).map(function (k) { return Math.round(TP.pos[k].y); });
        info.push('aligned ys=' + JSON.stringify(ys.filter(function (v, i, a) { return a.indexOf(v) === i; })));
        tpUndo(); return later(800);
      }).then(function () {
        tpClearSel(); tpStartAddNode();
        byId('tpNName').value = '出口防火墙'; byId('tpNKind').value = 'firewall'; byId('tpNIP').value = '10.0.0.254';
        tpSaveNode(); return later(2000);
      }).then(function () {
        info.push('manual=' + TP.data.manual_nodes.length);
        tpToggleLinkMode(); tpLinkPick('m:' + TP.data.manual_nodes[0].id); tpLinkPick('10.0.0.1');
        byId('tpLAP').value = 'ge0/0'; byId('tpLBP').value = '10GE1/0/24'; byId('tpLNote').value = '出口'; tpSaveLink();
        return later(2000);
      }).then(function () {
        info.push('links=' + TP.links.map(function (l) { return l.source; }).join(','));
        var cdp = TP.links.filter(function (l) { return l.source === 'lldp'; })[0];
        tpOpenSide({ t: 'link', l: cdp }); tpHideLink();
        return later(2000);
      }).then(function () {
        info.push('hidden=' + TP.data.hidden_links + ' links=' + TP.links.length);
        tpAutoLayout('tree');
        return later(1500);
      });
    },
    vendors: function () {
      return login().then(function () { openSettings(); return later(1000); }).then(function () {
        SET.sec = SET_SCHEMA.findIndex(function (s) { return s.custom === 'vendors'; }); renderSettings();
        return later(1200);
      }).then(function () {
        info.push('vendors=' + VEN.list.map(function (v) { return v.id; }).join(','));
        venAdd(); venSet('id', 'zte'); venSet('name', '中兴 ZTE'); venSet('enterprises', '3902'); venSet('keywords', 'zxr10');
        venArrAdd('cpu', '.1.3.6.1.4.1.3902.3.3.1.1.1.4');
        saveSettings();
        return later(2000, function () { info.push('msg=' + byId('setMsg').innerText + ' list=' + VEN.list.map(function (v) { return v.id + (v.modified ? '*' : ''); }).join(',')); venTry('.1.3.6.1.2.1.1.5.0'); });
      }).then(function () { return later(4000, function () { info.push('test=' + JSON.stringify(VEN.test.res)); }); });
    },
    bsCfg: function () {
      openBigScreen();
      return later(2500, function () { bsToggleCfg(); });
    }
  };
  setTimeout(function () {
    var fn = steps[act];
    var p = fn ? Promise.resolve().then(fn) : later(2000);
    p.catch(function (e) { errs.push('STEP ' + (e && e.stack || e)); }).then(function () { return later(1500); }).then(function () { report('done'); });
  }, 1200);
})();
