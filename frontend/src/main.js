import './style.css';
import { initCompactMode, initNotifyMode } from './modes.js';

function waitForWails(cb) {
  if (window.go?.main?.App || window.go?.main?.NotifyApp || window.go?.main?.CompactApp) { cb(); return; }
  setTimeout(() => waitForWails(cb), 50);
}

waitForWails(async () => {
  // Compact mode — standalone window
  if (window.go?.main?.CompactApp) {
    await initCompactMode();
    return;
  }

  // Notify popup mode — standalone window
  if (window.go?.main?.NotifyApp) {
    await initNotifyMode();
    return;
  }

  // Main editor mode
  try {
    await import('./editor.js');
  } catch(e) {
    document.body.innerHTML = '<pre style="color:red;padding:20px">editor.js load error:\n' + e.stack + '</pre>';
    return;
  }

  const go = window.go.main.App;

  const args = await go.GetStartupArgs();
  if (args.project && args.session) {
    await window.loadProjects();
    const pel = document.querySelector(`#project-list [data-id="${args.project}"]`);
    if (pel) { pel.classList.add('active'); window.setCurrentProject(args.project); }
    await window.loadSessions(args.project);
    const sel = document.querySelector(`#session-list [data-id="${args.session}"]`);
    if (sel) { sel.classList.add('active'); await window.selectSession(args.session, sel); }
  } else {
    window.initFromHash();
  }
  window.checkUpdate();
});
