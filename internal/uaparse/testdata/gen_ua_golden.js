// Generates testdata/ua_golden.json from the REAL Kutt dependencies.
// Run inside the kutt-copy dir: node gen_ua_golden.js uas.txt > ua_golden.json
const fs = require("fs");
const useragent = require("express-useragent").default;
const { isbot } = require("isbot");

// Copied verbatim from kutt server/utils/utils.js:360-394
function getUseragentBrowser(agent) {
  if (agent.isIE) return "ie";
  else if (agent.isFirefox) return "firefox";
  else if (agent.isChrome) return "chrome";
  else if (agent.isOpera) return "opera";
  else if (agent.isEdge) return "edge";
  else return "other";
}
function getUseragentOS(agent) {
  if (agent.isWindows) return "windows";
  else if (agent.isMac && agent.isDesktop) return "macos";
  else if (agent.isiPhone || agent.isiPad || agent.isiPod) return "ios";
  else if (agent.isMac && agent.isMobile) return "ios";
  else if (agent.isAndroid) return "android";
  else if (agent.isLinux) return "linux";
  else return "other";
}

const lines = fs.readFileSync(process.argv[2], "utf8").split("\n").filter((l) => l.length > 0);
lines.push("");
const out = lines.map((ua) => {
  const agent = useragent.parse(ua);
  return { ua, browser: getUseragentBrowser(agent), os: getUseragentOS(agent), bot: isbot(ua) };
});
process.stdout.write(JSON.stringify(out, null, 1));
