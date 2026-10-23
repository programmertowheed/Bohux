import { fromBinary } from "@bufbuild/protobuf";
import { ExecRequestSchema } from "./gen/bohux/v1/workload_pb";
import { closeMessage, execSocketURL, resizeFrame, splitCommand, startFrame, stdinFrame } from "./terminal";

test("the socket follows the page's scheme and host", () => {
  expect(execSocketURL({ protocol: "https:", host: "bohux.example" })).toBe("wss://bohux.example/sockets/v1/exec");
  expect(execSocketURL({ protocol: "http:", host: "127.0.0.1:7400" })).toBe("ws://127.0.0.1:7400/sockets/v1/exec");
});

test("frames are ExecRequest messages", () => {
  const start = fromBinary(ExecRequestSchema, startFrame("app-1", ["sh", "-l"], 24, 80));
  expect(start.msg.case).toBe("start");
  if (start.msg.case === "start") {
    expect(start.msg.value.appId).toBe("app-1");
    expect(start.msg.value.command).toEqual(["sh", "-l"]);
    expect(start.msg.value.tty).toBe(true);
    expect(start.msg.value.size).toMatchObject({ rows: 24, cols: 80 });
  }
  const stdin = fromBinary(ExecRequestSchema, stdinFrame("ls\r"));
  expect(stdin.msg.case === "stdin" && new TextDecoder().decode(stdin.msg.value)).toBe("ls\r");
  const resize = fromBinary(ExecRequestSchema, resizeFrame(30, 100));
  expect(resize.msg.case === "resize" && resize.msg.value).toMatchObject({ rows: 30, cols: 100 });
});

test("commands split on whitespace", () => {
  expect(splitCommand("  sh  -c   ls ")).toEqual(["sh", "-c", "ls"]);
  expect(splitCommand("   ")).toEqual([]);
});

test("close codes explain the ending", () => {
  expect(closeMessage(1000, "")).toBeUndefined();
  expect(closeMessage(4014, "the app's node is not connected")).toBe("the app's node is not connected");
  expect(closeMessage(4007, "")).toBe("permission denied");
  expect(closeMessage(1006, "")).toBe("connection closed (1006)");
});
