// ClientLogic.h
#pragma once
#include "../SDK.hpp"

extern bool LoginCompleted;
extern bool ReadyToAutoconnect;

void ConnectToMatch();
void AutoConnectToMatchFromCmdline();
