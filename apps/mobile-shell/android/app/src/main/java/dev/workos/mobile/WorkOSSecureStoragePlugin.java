package dev.workos.mobile;

import com.getcapacitor.JSObject;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;

@CapacitorPlugin(name = "WorkOSSecureStorage")
public final class WorkOSSecureStoragePlugin extends Plugin {
    private DeviceKeyVault vault;

    @Override
    public void load() {
        vault = new DeviceKeyVault(getContext());
    }

    @PluginMethod
    public void getStatus(PluginCall call) {
        try {
            vault.checkAvailable();
            call.resolve(new JSObject().put("secure", true));
        } catch (Exception error) {
            call.resolve(new JSObject().put("secure", false));
        }
    }

    @PluginMethod
    public void get(PluginCall call) {
        try {
            String value = vault.get(call.getString("key"));
            if (value == null) {
                call.reject("Item with given key does not exist");
                return;
            }
            call.resolve(new JSObject().put("value", value));
        } catch (Exception error) {
            call.reject("native secure storage read failed");
        }
    }

    @PluginMethod
    public void set(PluginCall call) {
        try {
            vault.set(call.getString("key"), call.getString("value"));
            call.resolve(new JSObject().put("value", true));
        } catch (Exception error) {
            call.reject("native secure storage write failed");
        }
    }

    @PluginMethod
    public void remove(PluginCall call) {
        try {
            vault.remove(call.getString("key"));
            call.resolve(new JSObject().put("value", true));
        } catch (Exception error) {
            call.reject("native secure storage removal failed");
        }
    }
}
