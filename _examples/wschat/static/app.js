// The client of the room: one WebSocket, JSON both ways, and a reconnect when
// the server goes away. Every text of a user lands in textContent, never in
// innerHTML.
"use strict";

const $ = (id) => document.getElementById(id);
let name = "";
let sock = null;
let retry = 0;

function setStatus(text) {
	$("status").textContent = text;
}

function connect() {
	const scheme = location.protocol === "https:" ? "wss:" : "ws:";
	const url = `${scheme}//${location.host}/ws?name=${encodeURIComponent(name)}`;
	sock = new WebSocket(url, "chat.v1");

	sock.onopen = () => {
		retry = 0;
		setStatus("connected");
	};
	sock.onmessage = (e) => show(JSON.parse(e.data));
	sock.onclose = (e) => {
		sock = null;
		if (!name) return; // the visitor left
		// 1012 is the drain of the server: the next one is up in a moment.
		// Anything else backs off, up to ten seconds.
		const wait = e.code === 1012 ? 250 : Math.min(10000, 500 * 2 ** retry++);
		setStatus(`reconnecting (${e.code})`);
		setTimeout(connect, wait);
	};
}

function show(m) {
	const log = $("log");
	let el;
	if (m.kind === "notice") {
		el = document.createElement("p");
		el.className = "notice";
		const who = document.createElement("strong");
		who.textContent = m.author;
		el.append(who, " " + m.text);
	} else {
		el = document.createElement("article");
		el.className = m.author === name ? "msg own" : "msg";
		const who = document.createElement("span");
		who.className = "who";
		who.textContent = m.author;
		const text = document.createElement("span");
		text.textContent = m.text;
		const at = document.createElement("time");
		at.dateTime = m.at;
		at.textContent = new Date(m.at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
		el.append(who, text, at);
	}
	log.append(el);
	log.scrollTop = log.scrollHeight;
}

$("join").addEventListener("submit", (e) => {
	e.preventDefault();
	name = e.target.name.value.trim();
	if (!name) return;
	$("me").textContent = name;
	$("door").hidden = true;
	$("room").hidden = false;
	$("say").text.focus();
	connect();
});

$("say").addEventListener("submit", (e) => {
	e.preventDefault();
	const input = e.target.text;
	if (!sock || sock.readyState !== WebSocket.OPEN || !input.value.trim()) return;
	sock.send(JSON.stringify({ text: input.value }));
	input.value = "";
});

$("leave").addEventListener("click", () => {
	name = "";
	if (sock) sock.close(1000, "left");
	$("log").replaceChildren();
	$("room").hidden = true;
	$("door").hidden = false;
});
