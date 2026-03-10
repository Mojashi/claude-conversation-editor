export namespace main {
	
	export class ContentSummary {
	    types: string[];
	    text_preview: string;
	    size: number;
	
	    static createFrom(source: any = {}) {
	        return new ContentSummary(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.types = source["types"];
	        this.text_preview = source["text_preview"];
	        this.size = source["size"];
	    }
	}
	export class Message {
	    uuid: string;
	    parentUuid: string;
	    type: string;
	    role: string;
	    timestamp: string;
	    isSidechain: boolean;
	    content_summary: ContentSummary;
	    is_tool_only: boolean;
	    is_system: boolean;
	    model: string;
	    raw: number[];
	
	    static createFrom(source: any = {}) {
	        return new Message(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.uuid = source["uuid"];
	        this.parentUuid = source["parentUuid"];
	        this.type = source["type"];
	        this.role = source["role"];
	        this.timestamp = source["timestamp"];
	        this.isSidechain = source["isSidechain"];
	        this.content_summary = this.convertValues(source["content_summary"], ContentSummary);
	        this.is_tool_only = source["is_tool_only"];
	        this.is_system = source["is_system"];
	        this.model = source["model"];
	        this.raw = source["raw"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	export class Conversation {
	    messages: Message[];
	    total_size: number;
	    session_id: string;
	
	    static createFrom(source: any = {}) {
	        return new Conversation(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.messages = this.convertValues(source["messages"], Message);
	        this.total_size = source["total_size"];
	        this.session_id = source["session_id"];
	    }
	
		convertValues(a: any, classs: any, asMap: boolean = false): any {
		    if (!a) {
		        return a;
		    }
		    if (a.slice && a.map) {
		        return (a as any[]).map(elem => this.convertValues(elem, classs));
		    } else if ("object" === typeof a) {
		        if (asMap) {
		            for (const key of Object.keys(a)) {
		                a[key] = new classs(a[key]);
		            }
		            return a;
		        }
		        return new classs(a);
		    }
		    return a;
		}
	}
	
	export class Project {
	    id: string;
	    name: string;
	    session_count: number;
	    mtime: number;
	
	    static createFrom(source: any = {}) {
	        return new Project(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.name = source["name"];
	        this.session_count = source["session_count"];
	        this.mtime = source["mtime"];
	    }
	}
	export class SaveRequest {
	    keep_uuids: string[];
	
	    static createFrom(source: any = {}) {
	        return new SaveRequest(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.keep_uuids = source["keep_uuids"];
	    }
	}
	export class SaveResult {
	    success: boolean;
	    kept_lines: number;
	    new_size: number;
	    backup: string;
	
	    static createFrom(source: any = {}) {
	        return new SaveResult(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.success = source["success"];
	        this.kept_lines = source["kept_lines"];
	        this.new_size = source["new_size"];
	        this.backup = source["backup"];
	    }
	}
	export class Session {
	    id: string;
	    preview: string;
	    msg_count: number;
	    size: number;
	    mtime: number;
	
	    static createFrom(source: any = {}) {
	        return new Session(source);
	    }
	
	    constructor(source: any = {}) {
	        if ('string' === typeof source) source = JSON.parse(source);
	        this.id = source["id"];
	        this.preview = source["preview"];
	        this.msg_count = source["msg_count"];
	        this.size = source["size"];
	        this.mtime = source["mtime"];
	    }
	}

}

