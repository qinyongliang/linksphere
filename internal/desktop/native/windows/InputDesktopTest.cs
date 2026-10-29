// Owned test fixture: creates a private desktop but never switches input to it.
using System;
using System.Collections.Generic;
using System.Diagnostics;
using System.Linq;
using System.Reflection;
using System.Runtime.InteropServices;
using System.Threading;
using System.Web.Script.Serialization;
using System.Windows.Forms;

class InputDesktopTest {
 [DllImport("user32.dll",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr CreateDesktop(string name,IntPtr device,IntPtr mode,uint flags,uint access,IntPtr security);
 [DllImport("user32.dll",SetLastError=true)] [return:MarshalAs(UnmanagedType.Bool)] static extern bool SetThreadDesktop(IntPtr desktop);
 [DllImport("user32.dll")] [return:MarshalAs(UnmanagedType.Bool)] static extern bool CloseDesktop(IntPtr desktop);
 [DllImport("user32.dll")] static extern IntPtr GetThreadDesktop(uint thread);
 [DllImport("kernel32.dll")] static extern uint GetCurrentThreadId();
 [DllImport("user32.dll",EntryPoint="GetUserObjectInformationW",SetLastError=true)] [return:MarshalAs(UnmanagedType.Bool)] static extern bool GetUserObjectInformation(IntPtr handle,int index,out int value,uint length,out uint needed);
 static Type helper;static JavaScriptSerializer json=new JavaScriptSerializer {MaxJsonLength=16*1024*1024};
 static Dictionary<string,object> Map(object value){return (Dictionary<string,object>)value;}
 static Dictionary<string,object> D(params object[] pairs){var result=new Dictionary<string,object>();for(int i=0;i<pairs.Length;i+=2)result.Add((string)pairs[i],pairs[i+1]);return result;}
 static object Native(string name,params object[] args){try{return helper.GetMethod(name,BindingFlags.Static|BindingFlags.NonPublic).Invoke(null,args);}catch(TargetInvocationException e){throw e.InnerException;}}
 static Dictionary<string,object> Call(string name,Dictionary<string,object> p){return Map(json.DeserializeObject(json.Serialize(Native("Dispatch",name,p))));}
 static bool Input(){int input;uint needed;return GetUserObjectInformation(GetThreadDesktop(GetCurrentThreadId()),6,out input,4,out needed)&&input!=0;}
 static void Check(bool condition,string message){if(!condition)throw new Exception(message);}
 static void Refused(System.Action operation,string name){try{operation();throw new Exception(name+" unexpectedly succeeded");}catch(Exception e){Check(e.Message.Contains("input desktop unavailable")&&e.Message.Contains("unlock"),name+" did not explain input desktop availability: "+e.Message);}}
 static Dictionary<string,object> Observe(Dictionary<string,object> window,bool screenshot){return Call("get_window_state",D("window",window,"include_text",true,"include_screenshot",screenshot,"mode","background"));}
 static Dictionary<string,object> Element(Dictionary<string,object> state,string name){return ((object[])state["elements"]).Select(Map).First(e=>(string)e["name"]==name);}
 static Dictionary<string,object> Operation(Dictionary<string,object> window,Dictionary<string,object> state){return D("window",window,"snapshot_id",state["snapshot_id"]);}
 static void NonInputFixture(IntPtr desktop){
  Check(SetThreadDesktop(desktop),"cannot attach test worker to owned desktop");
  Check(!Input(),"owned desktop unexpectedly receives input");
  var status=Call("status",D());var permissions=Map(status["permissions"]);
  Check(!(bool)status["interactive"]&&!(bool)permissions["screen_capture"],"non-input desktop was reported as interactive/capturable");
  Check((bool)permissions["accessibility"]&&(bool)status["background_supported"],"non-input desktop disabled window accessibility/background support");
  Check(!(bool)Native("ReceivesInput",IntPtr.Zero)&&!(bool)Native("ReceivesInput",new IntPtr(-1)),"failed input-desktop probes must be unavailable");
  Dictionary<string,object> window=null;
  for(int attempt=0;attempt<60&&window==null;attempt++){
   foreach(Form form in Application.OpenForms)if(form.IsHandleCreated&&form.Visible){window=D("id",form.Handle.ToInt64().ToString(),"app",Process.GetCurrentProcess().Id.ToString(),"title",form.Text);break;}
   if(window==null)Thread.Sleep(50);
  }
  Check(window!=null,"owned fixture window unavailable");
  var state=Observe(window,true);Check((string)Map(state["image"])["capture_method"]=="print_window","non-input desktop lost window capture");
  var p=Operation(window,state);p["mode"]="background";p["element_index"]=Element(state,"Fixture text")["index"];p["text"]="inactive desktop sample";
  Check((string)Call("set_value",p)["operation_method"]=="uia.value","non-input desktop lost semantic value write");
  state=Observe(window,false);Check((string)Element(state,"Fixture text")["value"]=="inactive desktop sample","semantic write had no effect");
  foreach(string method in new[]{"activate_window","press_key","type_text","click","drag","scroll","perform_action"}){
   state=Observe(window,false);p=Operation(window,state);p["x"]=30;p["y"]=30;p["to_x"]=40;p["to_y"]=40;p["delta_y"]=120;p["key"]="Ctrl+a";p["text"]="forbidden";
   if(method=="perform_action"){p["element_index"]=Element(state,"Fixture text")["index"];p["action"]="focus";}
   Refused(()=>Call(method,p),method);
  }
  Refused(()=>Call("screenshot",D()),"display screenshot");
  state=Observe(window,true);Check((string)Element(state,"Fixture text")["value"]=="inactive desktop sample","refused foreground operation changed fixture");
  Console.WriteLine("non-input desktop status, foreground guards and background fixture passed");
 }
 [MTAThread] static int Main(string[] args){
  try{
   helper=Assembly.LoadFrom(args[0]).GetType("DesktopHelper");
   if(Process.GetCurrentProcess().SessionId==0){Console.WriteLine("Session 0 fixture skipped; requires a logged-in user session");return 0;}
   var status=Call("status",D());bool input=Input();
   Check((bool)status["interactive"]==input&&(bool)Map(status["permissions"])["screen_capture"]==input,"current desktop availability differs from native input state");
   if(input)Console.WriteLine("active input desktop status passed");
   IntPtr desktop=CreateDesktop("gosshd-input-test-"+Guid.NewGuid().ToString("N"),IntPtr.Zero,IntPtr.Zero,0,0xC7,IntPtr.Zero);
   Check(desktop!=IntPtr.Zero,"create private test desktop failed: "+Marshal.GetLastWin32Error());
   Exception failure=null;Thread fixture=null,worker=null;
   try{
    // CLR STA initialization can create a window before SetThreadDesktop. The
    // plain owned controls need no OLE features, so attach on an MTA thread.
    fixture=new Thread(()=>{try{Check(SetThreadDesktop(desktop),"cannot attach owned fixture desktop: "+Marshal.GetLastWin32Error());typeof(Fixture).GetMethod("Main",BindingFlags.Static|BindingFlags.NonPublic).Invoke(null,new object[]{new[]{"--background"}});}catch(Exception e){Interlocked.CompareExchange(ref failure,e,null);Console.Error.WriteLine("fixture: "+e);}});fixture.IsBackground=true;fixture.SetApartmentState(ApartmentState.MTA);fixture.Start();
    worker=new Thread(()=>{try{NonInputFixture(desktop);}catch(Exception e){Interlocked.CompareExchange(ref failure,e,null);}finally{foreach(Form form in Application.OpenForms.Cast<Form>().ToArray()){try{form.BeginInvoke(new System.Action(form.Close));}catch{}}}});worker.IsBackground=true;worker.SetApartmentState(ApartmentState.MTA);worker.Start();
    Check(worker.Join(25000),"private input-desktop checks timed out");Check(fixture.Join(5000),"owned fixture did not close");if(failure!=null)throw failure;
   }finally{if((fixture==null||!fixture.IsAlive)&&(worker==null||!worker.IsAlive))CloseDesktop(desktop);}
   if(input){Native("RequireInputDesktop");Console.WriteLine("active input desktop preflight passed");}
   return 0;
  }catch(Exception e){Console.Error.WriteLine(e);return 1;}
 }
}
